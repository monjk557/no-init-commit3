// Package gitsig 는 조회 화면이 그리는 커밋 서명 배지의 계약과 판정 캐시를 담는다.
//
// push 시점의 판정은 ruleguard 가 한다. 여기는 그 결과를 화면 계약으로 옮기고, 캐시에
// 없는 커밋에 한해 "서명이 있기는 한가" 까지만 다시 확인하는 조회 경로다.
package gitsig

import "time"

// StateEnum 는 배지가 표시하는 네 가지 판정이다.
//
// git 이 돌려주는 한 글자(%G?)를 그대로 노출하지 않는다. 그 글자는 앵커 유무에 따라
// 같은 커밋에서도 뒤집히기 때문에, 화면이 알아야 하는 것은 "믿을 수 있는가" 와
// "왜 그렇게 판정했는가" 뿐이다.
type StateEnum string

const (
	// StateVerified 는 세 조건을 모두 만족한 경우다: 서명이 유효하고, 키가 등록되어
	// 있으며, 커밋의 committer 이메일이 그 키 주인의 것이다.
	StateVerified StateEnum = "verified"
	// StateUnverified 는 서명은 있으나 신뢰할 수 없는 경우다. 변조되었거나, 등록되지
	// 않은 키이거나, 이메일이 키 주인과 다르다.
	StateUnverified StateEnum = "unverified"
	// StateUnsigned 는 서명 자체가 없는 경우다. 위반이 아니라 부재이므로 기본 설정에서는
	// 배지를 그리지 않는다.
	StateUnsigned StateEnum = "unsigned"
	// StateKeyUnavailable 은 판정을 내리지 못한 경우다. 서명은 있지만 신뢰 앵커가 없어
	// 확인할 수 없었다는 뜻이며, unverified 와 구분해야 한다. 확인하지 못한 것을
	// 확인해서 틀렸다고 표시하면 사용자는 자기 키를 의심하게 된다.
	StateKeyUnavailable StateEnum = "key-unavailable"
)

// SignerTypeEnum 은 서명 주체다. 서버가 만든 머지 커밋과 사용자의 커밋을 화면에서 다르게
// 설명해야 한다 — 사용자는 자기가 서명하지 않은 커밋에 배지가 붙은 이유를 알아야 한다.
type SignerTypeEnum string

const (
	SignerUser   SignerTypeEnum = "user"
	SignerSystem SignerTypeEnum = "system"
)

// Reason 은 그렇게 판정한 근거다.
//
// 사람이 읽을 문구를 여기서 만들지 않는다. executor 는 사용자에게 보여줄 메시지를
// 만들지 않는다는 규칙(docs/principle/003)을 따르고, 문구는 화면이 렌더링한다.
// 서버가 문장을 내려보내면 다국어가 불가능해지고 문구 하나 고치는 데 배포가 필요하다.
//
// 값을 지금 전부 정의해 둔다. 소비자는 이 집합을 Literal 로 좁히므로, 나중에 값을
// 추가하면 그 값을 모르는 소비자에서 응답 전체가 거부된다.
type ReasonEnum string

const (
	// ReasonVerified 는 세 조건을 모두 만족한 경우다.
	ReasonVerified ReasonEnum = "verified"
	// ReasonUnsigned 는 서명이 없는 경우다.
	ReasonUnsigned ReasonEnum = "unsigned"
	// ReasonAnchorUnavailable 은 신뢰 앵커가 없어 판정하지 못한 경우다. 서명이
	// 틀렸다는 뜻이 아니다.
	ReasonAnchorUnavailable ReasonEnum = "anchor-unavailable"
	// ReasonUnknownKey 는 서명은 유효하나 등록되지 않은 키인 경우다.
	ReasonUnknownKey ReasonEnum = "unknown-key"
	// ReasonEmailMismatch 는 키 주인과 커밋의 committer 이메일이 다른 경우다.
	ReasonEmailMismatch ReasonEnum = "email-mismatch"
	// ReasonBadSignature 는 서명이 커밋 내용과 맞지 않는 경우다.
	ReasonBadSignature ReasonEnum = "bad-signature"
	// ReasonRevokedKey 는 폐기 시점 이후에 서명한 경우다. 폐기 이전 서명은 유효하다.
	ReasonRevokedKey ReasonEnum = "revoked-key"
	// ReasonExpiredKey 는 만료된 키로 서명한 경우다.
	ReasonExpiredKey ReasonEnum = "expired-key"
)

// Signer 는 서명에 쓰인 키와 그 주인이다. system 서명이면 Email 은 비어 있다.
type Signer struct {
	Email       string `json:"email"`
	Fingerprint string `json:"fingerprint"`
	KeyID       string `json:"key_id"`
}

// Verification 은 커밋 응답에 실리는 서명 판정이다.
//
// 배지 툴팁을 누르는 시점에 따로 조회하지 않고 커밋 페이로드에 실어 보낸다. 목록 한
// 화면에 수십 개의 배지가 붙는데 클릭마다 왕복하면 그 자체가 지연이 된다.
type Verification struct {
	State      StateEnum      `json:"state"`
	SignerType SignerTypeEnum `json:"signer_type"`
	Signer     *Signer        `json:"signer"`
	// Reason 은 그렇게 판정한 근거다. 화면은 이 값으로 문구를 고른다.
	Reason ReasonEnum `json:"reason"`
	// VerifiedAt 은 판정을 내린 시각이다. 캐시에서 온 값인지 방금 계산한 값인지
	// 구분하는 유일한 단서다.
	VerifiedAt *time.Time `json:"verified_at"`
}
