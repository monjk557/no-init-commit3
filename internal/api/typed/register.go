// Package typed 의 Register* 함수군 — handler 시그니처 → registry capture +
// gin 라우트 등록을 한 번에 수행한다.
//
// 핵심: handler 의 generic param Req/Resp 가 등록 시점에 reflect.TypeOf 로
// 추출되어 Endpoint 에 저장된다. spec generator 는 registry 만 walk 하면 됨.
package typed

import (
	"net/http"
	"reflect"

	"github.com/gin-gonic/gin"
)

// reflectType 은 generic type T 의 reflect.Type 을 안전하게 추출한다.
// `reflect.TypeOf((*T)(nil)).Elem()` 은 T 가 interface 여도 정상 동작.
func reflectType[T any]() reflect.Type {
	return reflect.TypeOf((*T)(nil)).Elem()
}

// jsonSuccessResponses 는 200 + RespType 한 개를 가진 default Responses 를
// 만든다.
func jsonSuccessResponses(respType reflect.Type) []ResponseSpec {
	return []ResponseSpec{{
		Status:      http.StatusOK,
		RespType:    respType,
		ContentType: "application/json",
		Description: "Success",
	}}
}

// buildEndpoint 는 공통 Endpoint 초기화 + opts 적용을 수행한다.
func buildEndpoint(
	r gin.IRouter,
	method, ginPath, tag string,
	reqType reflect.Type,
	reqLoc RequestLocation,
	responses []ResponseSpec,
	contentType string,
	opts []EndpointOption,
) Endpoint {
	ep := Endpoint{
		Method:      method,
		Path:        ginToOpenAPIPath(absolutePath(r, ginPath)),
		Tag:         tag,
		ReqType:     reqType,
		ReqLocation: reqLoc,
		Responses:   responses,
		ContentType: contentType,
	}
	for _, opt := range opts {
		opt(&ep)
	}
	return ep
}

// RegisterJSON 은 application/json body 를 받는 endpoint 를 등록한다 (POST,
// PUT, PATCH 의 표준 패턴).
//
// handler 시그니처: func(*gin.Context, Req) (Resp, error)
//   - JSON bind 실패 → 400 + writeError
//   - handler error → mapAndWriteError (Endpoint.ErrorMapper 적용)
//   - 정상 → c.JSON(deriveStatus(ep), resp)
func RegisterJSON[Req any, Resp any](
	r gin.IRouter,
	method, path, tag string,
	handler func(*gin.Context, Req) (Resp, error),
	opts ...EndpointOption,
) {
	ep := buildEndpoint(
		r, method, path, tag,
		reflectType[Req](),
		LocationBodyJSON,
		jsonSuccessResponses(reflectType[Resp]()),
		"application/json",
		opts,
	)
	DefaultRegistry().Register(ep)

	r.Handle(method, path, func(c *gin.Context) {
		var req Req
		if err := c.ShouldBindJSON(&req); err != nil {
			writeError(c, http.StatusBadRequest, err.Error())
			return
		}
		resp, err := handler(c, req)
		if err != nil {
			mapAndWriteError(c, err, ep)
			return
		}
		c.JSON(deriveStatus(ep), resp)
	})
}

// RegisterGet 은 body / query 모두 없이 path/header context 만으로 동작하는
// GET endpoint 를 등록한다 (예: GET /state, GET /branches — query 는 handler
// 본문에서 c.Query 로 직접 읽음).
func RegisterGet[Resp any](
	r gin.IRouter,
	path, tag string,
	handler func(*gin.Context) (Resp, error),
	opts ...EndpointOption,
) {
	ep := buildEndpoint(
		r, http.MethodGet, path, tag,
		nil, // ReqType 없음
		LocationNone,
		jsonSuccessResponses(reflectType[Resp]()),
		"application/json",
		opts,
	)
	DefaultRegistry().Register(ep)

	r.Handle(http.MethodGet, path, func(c *gin.Context) {
		resp, err := handler(c)
		if err != nil {
			mapAndWriteError(c, err, ep)
			return
		}
		c.JSON(deriveStatus(ep), resp)
	})
}

// RegisterGetWithQuery 는 query string 을 Query struct 로 바인딩하는 GET
// endpoint 를 등록한다. gin 의 form/binding 태그를 따른다.
func RegisterGetWithQuery[Query any, Resp any](
	r gin.IRouter,
	path, tag string,
	handler func(*gin.Context, Query) (Resp, error),
	opts ...EndpointOption,
) {
	ep := buildEndpoint(
		r, http.MethodGet, path, tag,
		reflectType[Query](),
		LocationQuery,
		jsonSuccessResponses(reflectType[Resp]()),
		"application/json",
		opts,
	)
	DefaultRegistry().Register(ep)

	r.Handle(http.MethodGet, path, func(c *gin.Context) {
		var q Query
		if err := c.ShouldBindQuery(&q); err != nil {
			writeError(c, http.StatusBadRequest, err.Error())
			return
		}
		resp, err := handler(c, q)
		if err != nil {
			mapAndWriteError(c, err, ep)
			return
		}
		c.JSON(deriveStatus(ep), resp)
	})
}

// RegisterNoBody 는 body 가 없는 임의 method (DELETE, no-body POST 등) 를
// 등록한다.
func RegisterNoBody[Resp any](
	r gin.IRouter,
	method, path, tag string,
	handler func(*gin.Context) (Resp, error),
	opts ...EndpointOption,
) {
	ep := buildEndpoint(
		r, method, path, tag,
		nil,
		LocationNone,
		jsonSuccessResponses(reflectType[Resp]()),
		"application/json",
		opts,
	)
	DefaultRegistry().Register(ep)

	r.Handle(method, path, func(c *gin.Context) {
		resp, err := handler(c)
		if err != nil {
			mapAndWriteError(c, err, ep)
			return
		}
		c.JSON(deriveStatus(ep), resp)
	})
}

// RegisterRaw 는 binary streaming endpoint 를 등록한다 (예: blob/raw,
// LFS storage upload/download). handler 가 c.Data / c.Writer 를 직접 다루며,
// JSON wrapper / error mapper 는 적용되지 않는다.
//
// registry 에는 ReqLocation=LocationRawBytes + ContentType 으로 기록되어
// spec 에 `application/octet-stream` 등 binary content type 으로 노출된다.
func RegisterRaw(
	r gin.IRouter,
	method, path, tag string,
	handler gin.HandlerFunc,
	contentType string,
	opts ...EndpointOption,
) {
	responses := []ResponseSpec{{
		Status:      http.StatusOK,
		ContentType: contentType,
		Description: "Success",
	}}
	ep := buildEndpoint(
		r, method, path, tag,
		nil,
		LocationRawBytes,
		responses,
		contentType,
		opts,
	)
	DefaultRegistry().Register(ep)
	r.Handle(method, path, handler)
}
