package config

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"

	"github.com/cloudwego/kitex/pkg/klog"
	"gopkg.in/yaml.v3"
)

// event 暂存安全的启动日志，待统一 logger 生效后输出。
type event struct {
	format    string
	arguments []any
}

// snapshot 保存进程内唯一的合并配置和同一 Consul 客户端，字段均不向业务公开。
type snapshot struct {
	settings    Settings
	application *applicationConfig
	body        []byte
}

// Store 串行完成首次加载，成功后不读取环境变量、不再访问 Consul 或监听配置。
type Store struct {
	mu       sync.Mutex
	snapshot *snapshot
	events   []event
	reported bool
}

// NewStore 创建无 I/O 副作用的独立配置管理器。
func NewStore() *Store { return &Store{} }

// read 在持锁期间完成一次加载；失败不公布部分配置，并保留可安全输出的诊断。
func (s *Store) read(ctx context.Context, lookup func(string) (string, bool)) (*snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.snapshot != nil {
		return s.snapshot, nil
	}
	events := []event{}
	record := func(format string, args ...any) { events = append(events, event{format, append([]any(nil), args...)}) }
	value, err := readSnapshot(ctx, lookup, record)
	if err != nil {
		for _, item := range events {
			klog.Infof(item.format, item.arguments...)
		}
		klog.Errorf("core: 配置加载失败：%v", err)
		return nil, err
	}
	s.snapshot, s.events = value, events
	return value, nil
}

// Apply 将同一快照下发给显式接入的公共包，日志配置生效后才重放加载日志。
func (s *Store) Apply(ctx context.Context, lookup func(string) (string, bool), logging, monitor func([]byte) error, additional ...Component) (Settings, error) {
	value, err := s.read(ctx, lookup)
	if err != nil {
		return Settings{}, err
	}
	apply := func(component Component) error {
		if component.Configure == nil {
			return nil
		}
		var body []byte
		if data, ok := value.application.values[component.Name]; ok {
			body, err = yaml.Marshal(data)
			if err != nil {
				return errors.New("core: 组件配置编码失败")
			}
		}
		if err := component.Configure(body); err != nil {
			return fmt.Errorf("core: configure %s: %w", component.Name, err)
		}
		return nil
	}
	if err = apply(Component{"logging", logging}); err != nil {
		return Settings{}, err
	}
	s.mu.Lock()
	var pending []event
	if !s.reported {
		pending = s.events
		s.reported = true
	}
	s.mu.Unlock()
	for _, item := range pending {
		klog.Infof(item.format, item.arguments...)
	}
	for _, component := range append([]Component{{"monitor", monitor}}, additional...) {
		if err = apply(component); err != nil {
			return Settings{}, err
		}
	}
	value.application.logOverrides()
	source := "默认值"
	if service := value.application.sources["service"]; service != nil {
		if port := service.children["port"]; port != nil {
			source = port.file
		}
	}
	klog.Infof("core: 服务配置生效：name=[%s] port=[%d] port_source=[%s]", value.settings.ServiceName, value.settings.Port, source)
	return value.settings, nil
}

// View 仅向业务暴露配置映射能力，不提供原始 map、写入或重载入口。
type View struct {
	store  *Store
	lookup func(string) (string, bool)
}

// NewView 创建同一管理器的只读视图，不触发配置读取。
func NewView(store *Store, lookup func(string) (string, bool)) *View {
	return &View{store: store, lookup: lookup}
}

// Decode 将快照映射到独立对象，调用者修改该对象不会改变公共包或其他调用者的配置。
func (v *View) Decode(target any) error {
	dest := reflect.ValueOf(target)
	if !dest.IsValid() || dest.Kind() != reflect.Pointer || dest.IsNil() || (dest.Elem().Kind() != reflect.Struct && dest.Elem().Kind() != reflect.Map) {
		return errors.New("core: 配置目标必须为非空结构体或 map 指针")
	}
	value, err := v.store.read(context.Background(), v.lookup)
	if err != nil {
		return err
	}
	body := value.body
	if dest.Elem().Kind() == reflect.Struct {
		// 只选择业务类型声明的根节点，框架配置仍由对应公共包处理；节点内部严格校验字段。
		selected := make(map[string]any)
		for _, name := range rootFields(dest.Elem().Type()) {
			if data, ok := value.application.values[name]; ok {
				selected[name] = data
			}
		}
		body, err = yaml.Marshal(selected)
		if err != nil {
			return errors.New("core: 业务配置编码失败")
		}
	}
	copy := reflect.New(dest.Elem().Type())
	decoder := yaml.NewDecoder(bytes.NewReader(body))
	decoder.KnownFields(true)
	if decoder.Decode(copy.Interface()) != nil {
		return errors.New("core: 业务配置字段或类型无效")
	}
	dest.Elem().Set(copy.Elem())
	return nil
}

// rootFields 收集导出字段的 YAML 根名称，支持内联结构体，不暴露内部配置引用。
func rootFields(t reflect.Type) []string {
	var fields []string
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if field.PkgPath != "" {
			continue
		}
		tag := strings.Split(field.Tag.Get("yaml"), ",")
		if tag[0] == "-" {
			continue
		}
		inline := false
		for _, option := range tag[1:] {
			inline = inline || option == "inline"
		}
		nested := field.Type
		if nested.Kind() == reflect.Pointer {
			nested = nested.Elem()
		}
		if inline && nested.Kind() == reflect.Struct {
			fields = append(fields, rootFields(nested)...)
			continue
		}
		name := tag[0]
		if name == "" {
			name = strings.ToLower(field.Name)
		}
		fields = append(fields, name)
	}
	return fields
}

// ValidateIdentity 阻止配置预读与正式启动使用不同的应用身份，避免复用另一应用的快照。
func (s *Store) ValidateIdentity(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.snapshot != nil && s.snapshot.settings.ServiceName != name {
		return errors.New("core: 服务身份在配置加载后发生变化")
	}
	return nil
}
