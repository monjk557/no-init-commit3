// Package typed 의 EndpointOption 단위 테스트.
//
// 각 With* helper 가 Endpoint 의 정확한 필드를 변형하는지 + 누적 적용
// (compose) 동작을 검증한다.
package typed

import (
	"errors"
	"reflect"
	"testing"
)

func TestWithStatus_OverridesSuccessStatus(t *testing.T) {
	var e Endpoint
	WithStatus(201)(&e)
	if e.SuccessStatus != 201 {
		t.Errorf("SuccessStatus = %d, want 201", e.SuccessStatus)
	}
}

func TestWithResponse_AppendsMultiple(t *testing.T) {
	type Resp struct{ X int }
	var e Endpoint
	WithResponse(404, nil, "Not Found")(&e)
	WithResponse(409, reflect.TypeOf(Resp{}), "Conflict")(&e)

	if len(e.Responses) != 2 {
		t.Fatalf("Responses len = %d, want 2", len(e.Responses))
	}
	if e.Responses[0].Status != 404 || e.Responses[0].Description != "Not Found" {
		t.Errorf("Responses[0] = %+v", e.Responses[0])
	}
	if e.Responses[1].Status != 409 || e.Responses[1].RespType != reflect.TypeOf(Resp{}) {
		t.Errorf("Responses[1] = %+v", e.Responses[1])
	}
}

func TestWithErrorMapper_StoresFn(t *testing.T) {
	var e Endpoint
	sentinel := errors.New("boom")
	WithErrorMapper(func(err error) (int, any) {
		if errors.Is(err, sentinel) {
			return 418, "teapot"
		}
		return 500, nil
	})(&e)

	if e.ErrorMapper == nil {
		t.Fatal("ErrorMapper not set")
	}
	status, body := e.ErrorMapper(sentinel)
	if status != 418 || body != "teapot" {
		t.Errorf("ErrorMapper(sentinel) = (%d, %v); want (418, \"teapot\")", status, body)
	}

	// other 에러는 default branch
	statusOther, bodyOther := e.ErrorMapper(errors.New("other"))
	if statusOther != 500 || bodyOther != nil {
		t.Errorf("ErrorMapper(other) = (%d, %v); want (500, nil)", statusOther, bodyOther)
	}
}

func TestWithContentType_Override(t *testing.T) {
	var e Endpoint
	WithContentType("application/vnd.git-lfs+json")(&e)
	if e.ContentType != "application/vnd.git-lfs+json" {
		t.Errorf("ContentType = %q", e.ContentType)
	}
}

func TestWithSummary(t *testing.T) {
	var e Endpoint
	WithSummary("Get branches")(&e)
	if e.Summary != "Get branches" {
		t.Errorf("Summary = %q", e.Summary)
	}
}

func TestWithDescription(t *testing.T) {
	var e Endpoint
	WithDescription("Returns the list of branches")(&e)
	if e.Description != "Returns the list of branches" {
		t.Errorf("Description = %q", e.Description)
	}
}

func TestWithDeprecated(t *testing.T) {
	var e Endpoint
	if e.Deprecated {
		t.Fatal("default should not be deprecated")
	}
	WithDeprecated()(&e)
	if !e.Deprecated {
		t.Errorf("Deprecated should be true after WithDeprecated()")
	}
}

// 여러 옵션이 Endpoint 같은 인스턴스에 중첩 적용될 때 모두 보존되어야 한다.
func TestOptions_Cumulative(t *testing.T) {
	var e Endpoint
	opts := []EndpointOption{
		WithStatus(201),
		WithSummary("S"),
		WithDescription("D"),
		WithResponse(404, nil, "Not Found"),
		WithResponse(409, nil, "Conflict"),
		WithDeprecated(),
		WithContentType("text/plain"),
		WithErrorMapper(func(error) (int, any) { return 500, nil }),
	}
	for _, opt := range opts {
		opt(&e)
	}

	switch {
	case e.SuccessStatus != 201:
		t.Errorf("SuccessStatus = %d", e.SuccessStatus)
	case e.Summary != "S":
		t.Errorf("Summary = %q", e.Summary)
	case e.Description != "D":
		t.Errorf("Description = %q", e.Description)
	case len(e.Responses) != 2:
		t.Errorf("Responses len = %d", len(e.Responses))
	case !e.Deprecated:
		t.Error("Deprecated should be true")
	case e.ContentType != "text/plain":
		t.Errorf("ContentType = %q", e.ContentType)
	case e.ErrorMapper == nil:
		t.Error("ErrorMapper should be set")
	}
}

func TestWithRequestBody_SetsReqTypeAndLocation(t *testing.T) {
	type MyBody struct {
		X int `json:"x"`
	}
	var e Endpoint
	WithRequestBody(reflect.TypeOf(MyBody{}))(&e)
	if e.ReqType != reflect.TypeOf(MyBody{}) {
		t.Errorf("ReqType = %v, want MyBody", e.ReqType)
	}
	if e.ReqLocation != LocationBodyJSON {
		t.Errorf("ReqLocation = %v, want LocationBodyJSON", e.ReqLocation)
	}
}

func TestWithRequestBody_DoesNotDowngradeOtherLocations(t *testing.T) {
	type MyBody struct {
		X int `json:"x"`
	}
	e := Endpoint{ReqLocation: LocationRawBytes}
	WithRequestBody(reflect.TypeOf(MyBody{}))(&e)
	if e.ReqLocation != LocationRawBytes {
		t.Errorf("ReqLocation downgraded from RawBytes to %v", e.ReqLocation)
	}
}

func TestWithExcludeFromSpec(t *testing.T) {
	var e Endpoint
	if e.ExcludeFromSpec {
		t.Fatal("default should not be excluded")
	}
	WithExcludeFromSpec()(&e)
	if !e.ExcludeFromSpec {
		t.Errorf("ExcludeFromSpec should be true after WithExcludeFromSpec()")
	}
}
