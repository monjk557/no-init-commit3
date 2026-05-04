// Package typed 의 Endpoint 메타데이터 옵션 (functional options 패턴).
//
// 각 With* helper 는 EndpointOption 을 반환하고, Register* 함수가 등록 시점에
// `opt(&endpoint)` 형태로 누적 적용한다.
package typed

import "reflect"

// EndpointOption 은 Endpoint 의 한 부분을 변형하는 함수다.
type EndpointOption func(*Endpoint)

// WithStatus 는 정상 응답의 HTTP status 를 override 한다 (예: POST /forks 의
// 201). 미지정 시 wrapper 가 기본 200 사용.
func WithStatus(code int) EndpointOption {
	return func(e *Endpoint) {
		e.SuccessStatus = code
	}
}

// WithResponse 는 추가 응답 status (4xx 등) 를 등록한다. 동일 endpoint 에
// 여러 번 호출하여 다중 가능 응답을 누적할 수 있다. respType 이 nil 이면
// 응답 body schema 없음 (description 만 노출).
func WithResponse(status int, respType reflect.Type, desc string) EndpointOption {
	return func(e *Endpoint) {
		e.Responses = append(e.Responses, ResponseSpec{
			Status:      status,
			RespType:    respType,
			ContentType: "application/json",
			Description: desc,
		})
	}
}

// WithErrorMapper 는 handler error → (status, body) 변환 함수를 설치한다.
// runtime wrapper 에서 호출되며 spec generator 는 이 함수를 직접 보지 않는다
// (가능 status 는 별도로 WithResponse 로 노출).
func WithErrorMapper(mapper func(error) (int, any)) EndpointOption {
	return func(e *Endpoint) {
		e.ErrorMapper = mapper
	}
}

// WithContentType 은 응답 Content-Type 을 override 한다 (예: LFS 의
// "application/vnd.git-lfs+json").
func WithContentType(ct string) EndpointOption {
	return func(e *Endpoint) {
		e.ContentType = ct
	}
}

// WithSummary 는 OpenAPI operation summary 를 설정한다.
func WithSummary(s string) EndpointOption {
	return func(e *Endpoint) {
		e.Summary = s
	}
}

// WithDescription 은 OpenAPI operation description 을 설정한다.
func WithDescription(s string) EndpointOption {
	return func(e *Endpoint) {
		e.Description = s
	}
}

// WithDeprecated 는 endpoint 를 OpenAPI 의 deprecated 플래그로 표시한다.
func WithDeprecated() EndpointOption {
	return func(e *Endpoint) {
		e.Deprecated = true
	}
}

// WithExcludeFromSpec 는 spec generator 가 이 endpoint 를 출력에서 제외하도록
// 표시한다. gin 런타임 라우트 등록은 그대로 유지되므로 기존 caller 는 계속
// 호출 가능. deprecated / 기능에서 빠진 endpoint 를 외부 OpenAPI 노출에서만
// 숨길 때 사용.
func WithExcludeFromSpec() EndpointOption {
	return func(e *Endpoint) {
		e.ExcludeFromSpec = true
	}
}

// WithRequestBody 는 endpoint 의 requestBody schema 를 명시 등록한다.
// RegisterJSON 은 generic Req 로 자동 추론하지만, RegisterNoBody / RegisterRaw
// 처럼 generic Req 가 없는 등록 함수에서도 spec 에는 body schema 를 노출하고
// 싶을 때 사용 (예: handler 본문에서 lenient JSON bind 를 하는 케이스).
//
// ReqLocation 이 LocationNone 이면 LocationBodyJSON 으로 승격.
func WithRequestBody(t reflect.Type) EndpointOption {
	return func(e *Endpoint) {
		e.ReqType = t
		if e.ReqLocation == LocationNone {
			e.ReqLocation = LocationBodyJSON
		}
	}
}
