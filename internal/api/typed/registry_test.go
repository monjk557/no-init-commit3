// Package typed 의 Registry 단위 테스트.
//
// 이 파일은 Phase 0.1 의 RED 단계를 정의한다 — Endpoint / Registry 가
// 아직 구현되지 않은 상태에서 의도된 인터페이스를 명세한다.
package typed

import (
	"reflect"
	"sync"
	"testing"
)

func TestRegistry_Empty(t *testing.T) {
	r := NewRegistry()
	count := 0
	r.Walk(func(Endpoint) { count++ })
	if count != 0 {
		t.Fatalf("expected empty registry, got %d endpoints", count)
	}
}

func TestRegistry_RegisterAndWalk(t *testing.T) {
	r := NewRegistry()
	ep := Endpoint{
		Method: "POST",
		Path:   "/repos/{owner}/{repo}/branches",
		Tag:    "Branches",
	}
	r.Register(ep)

	var visited []Endpoint
	r.Walk(func(e Endpoint) { visited = append(visited, e) })
	if len(visited) != 1 {
		t.Fatalf("expected 1 endpoint, got %d", len(visited))
	}
	if visited[0].Method != "POST" || visited[0].Path != ep.Path || visited[0].Tag != "Branches" {
		t.Errorf("endpoint mismatch: got %+v", visited[0])
	}
}

func TestRegistry_WalkOrder_MethodAscPathAsc(t *testing.T) {
	r := NewRegistry()
	// 등록 순서가 임의여도 Walk 는 method asc, path asc 로 정렬된 결과를 반환.
	for _, ep := range []Endpoint{
		{Method: "POST", Path: "/branches"},
		{Method: "GET", Path: "/tags"},
		{Method: "GET", Path: "/branches"},
		{Method: "DELETE", Path: "/branches/x"},
	} {
		r.Register(ep)
	}

	want := []struct{ method, path string }{
		{"DELETE", "/branches/x"},
		{"GET", "/branches"},
		{"GET", "/tags"},
		{"POST", "/branches"},
	}

	var got []Endpoint
	r.Walk(func(e Endpoint) { got = append(got, e) })

	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].Method != w.method || got[i].Path != w.path {
			t.Errorf("index %d: got %s %s, want %s %s",
				i, got[i].Method, got[i].Path, w.method, w.path)
		}
	}
}

func TestRegistry_Snapshot_IsCopy(t *testing.T) {
	r := NewRegistry()
	r.Register(Endpoint{Method: "GET", Path: "/a"})

	snap1 := r.Snapshot()
	if len(snap1) != 1 {
		t.Fatalf("snapshot length = %d, want 1", len(snap1))
	}

	// 반환된 slice 변형이 registry 내부에 영향을 주지 않아야 한다.
	snap1[0].Path = "/mutated"

	snap2 := r.Snapshot()
	if snap2[0].Path != "/a" {
		t.Errorf("registry was mutated via snapshot: %s", snap2[0].Path)
	}
}

func TestRegistry_ConcurrentRegister(t *testing.T) {
	r := NewRegistry()
	const N = 100
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			r.Register(Endpoint{
				Method: "GET",
				Path:   "/concurrent",
				Tag:    "Test",
			})
		}()
	}
	wg.Wait()

	snap := r.Snapshot()
	if len(snap) != N {
		t.Errorf("after concurrent Register: len = %d, want %d", len(snap), N)
	}
}

func TestDefaultRegistry_Singleton(t *testing.T) {
	r1 := DefaultRegistry()
	r2 := DefaultRegistry()
	if r1 != r2 {
		t.Errorf("DefaultRegistry should return the same instance")
	}
}

func TestEndpoint_ReqType_Preserved(t *testing.T) {
	type MyReq struct {
		Name string `json:"name"`
	}
	r := NewRegistry()
	r.Register(Endpoint{
		Method:  "POST",
		Path:    "/test",
		ReqType: reflect.TypeOf(MyReq{}),
	})

	snap := r.Snapshot()
	if snap[0].ReqType != reflect.TypeOf(MyReq{}) {
		t.Errorf("ReqType not preserved through Register/Snapshot: got %v", snap[0].ReqType)
	}
}

func TestEndpoint_Responses_Preserved(t *testing.T) {
	type MyResp struct {
		OK bool `json:"ok"`
	}
	r := NewRegistry()
	r.Register(Endpoint{
		Method: "GET",
		Path:   "/test",
		Responses: []ResponseSpec{
			{Status: 200, RespType: reflect.TypeOf(MyResp{}), ContentType: "application/json", Description: "Success"},
			{Status: 404, Description: "Not Found"},
		},
	})

	snap := r.Snapshot()
	if len(snap[0].Responses) != 2 {
		t.Fatalf("Responses len = %d, want 2", len(snap[0].Responses))
	}
	if snap[0].Responses[0].Status != 200 || snap[0].Responses[0].RespType != reflect.TypeOf(MyResp{}) {
		t.Errorf("Responses[0] mismatch: %+v", snap[0].Responses[0])
	}
	if snap[0].Responses[1].Status != 404 {
		t.Errorf("Responses[1] mismatch: %+v", snap[0].Responses[1])
	}
}

func TestEndpoint_RequestLocation_Constants(t *testing.T) {
	// 각 RequestLocation 상수가 distinct value 인지 확인.
	locs := []RequestLocation{
		LocationNone,
		LocationBodyJSON,
		LocationQuery,
		LocationMultipart,
		LocationRawBytes,
	}
	seen := make(map[RequestLocation]bool)
	for _, l := range locs {
		if seen[l] {
			t.Errorf("RequestLocation values are not distinct: %d duplicated", l)
		}
		seen[l] = true
	}
}

func TestSetDefaultRegistry_SwapAndRestore(t *testing.T) {
	orig := DefaultRegistry()
	fresh := NewRegistry()
	restore := SetDefaultRegistry(fresh)
	if DefaultRegistry() != fresh {
		t.Errorf("after swap: DefaultRegistry() != fresh")
	}
	restore()
	if DefaultRegistry() != orig {
		t.Errorf("after restore: DefaultRegistry() != orig")
	}
}
