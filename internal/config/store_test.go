package config

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// TestSnapshotSharedByBusinessAndComponents 验证业务预读与 Server 共用快照，各文件仅请求一次且对象相互隔离。
func TestSnapshotSharedByBusinessAndComponents(t *testing.T) {
	var reads atomic.Int32
	consul := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		body := "imports: [config/zmall/shared.yaml]\nauth: {issuer: application}\nservice: {port: 8888}"
		if strings.HasSuffix(r.URL.Path, "shared.yaml") {
			body = "auth: {issuer: imported, audience: test}\nlogging: {level: info}\ndatabase: {password: private-value}"
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{{"Value": []byte(body)}})
	}))
	defer consul.Close()
	env := lookup(environment(consul.URL))
	store := NewStore()
	view := NewView(store, env)
	type business struct {
		Auth struct {
			Issuer   string `yaml:"issuer"`
			Audience string `yaml:"audience"`
		} `yaml:"auth"`
	}
	var group sync.WaitGroup
	for range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			var cfg business
			if err := view.Decode(&cfg); err != nil || cfg.Auth.Issuer != "application" || cfg.Auth.Audience != "test" {
				t.Error("incorrect business snapshot")
			}
			cfg.Auth.Issuer = "mutated"
		}()
	}
	group.Wait()
	// 已加载后环境变化不会把当前进程切换到其他 Consul。
	view.lookup = func(string) (string, bool) { t.Error("snapshot re-read environment"); return "", false }
	configured := 0
	settings, err := store.Apply(context.Background(), view.lookup, nil, nil, Component{Name: "database", Configure: func(body []byte) error {
		configured++
		if !strings.Contains(string(body), "private-value") || strings.Contains(string(body), "issuer") {
			t.Error("component section isolation lost")
		}
		return nil
	}})
	if err != nil || settings.ServiceName != "zmall-product" || reads.Load() != 2 || configured != 1 {
		t.Fatalf("snapshot reused incorrectly: %v reads=%d", err, reads.Load())
	}
	var copy business
	if err := view.Decode(&copy); err != nil || copy.Auth.Issuer != "application" {
		t.Fatal("caller mutated stored configuration")
	}
}

// TestSnapshotDecodeStrictAndAtomic 验证非法映射不污染调用者、未知业务字段不被静默忽略。
func TestSnapshotDecodeStrictAndAtomic(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{"Value": []byte("auth: {issuer: test, unknown: private-value}\nmonitor: {port: 9091}")}})
	}))
	defer server.Close()
	view := NewView(NewStore(), lookup(environment(server.URL)))
	target := struct {
		Auth struct {
			Issuer string `yaml:"issuer"`
		} `yaml:"auth"`
	}{}
	target.Auth.Issuer = "original"
	if err := view.Decode(&target); err == nil || strings.Contains(err.Error(), "private-value") || target.Auth.Issuer != "original" {
		t.Fatal("unsafe or partial configuration mapping")
	}
	var mapped map[string]any
	if err := view.Decode(&mapped); err != nil {
		t.Fatal(err)
	}
	mapped["auth"].(map[string]any)["issuer"] = "changed"
	var again map[string]any
	if err := view.Decode(&again); err != nil || again["auth"].(map[string]any)["issuer"] != "test" {
		t.Fatal("map result mutated core snapshot")
	}
}
