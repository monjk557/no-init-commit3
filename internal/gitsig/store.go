package gitsig

import (
	"context"
	"time"

	"go.uber.org/zap"

	"github.com/collabops/git-executor/internal/cache"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/ruleguard"
)

// storeTTL 은 판정 캐시의 보관 기간이다. 판정이 뒤집히는 계기는 키 폐기와 이메일 변경인데
// 둘 다 즉시 반영될 필요는 없고, 만료되면 push 때 다시 채워진다.
const storeTTL = 30 * time.Minute

// storeSize 는 보관할 판정 수다. 목록 한 화면이 수십 건이라 여러 저장소가 섞여도
// 최근 조회분은 남는다.
const storeSize = 5000

// Store 는 커밋 서명 판정 캐시다.
//
// executor 에 테이블을 만들지 않는다. 판정은 커밋과 앵커에서 다시 계산되는 파생값이라
// 권위가 없고, 잃어버려도 push 때 다시 채워진다. 그래서 기존 MemoryCache 를 그대로 쓴다.
type Store struct {
	cache  *cache.MemoryCache[Verification]
	logger *logging.Logger
}

// NewStore 는 판정 캐시를 만든다. logger 가 nil 이면 아무것도 남기지 않는다.
func NewStore(logger *logging.Logger) *Store {
	return &Store{
		logger: logger,
		cache: cache.NewMemoryCache[Verification](&cache.MemoryCacheConfig{
			MaxSize:         storeSize,
			DefaultTTL:      storeTTL,
			CleanupInterval: time.Minute,
			EnableMetrics:   true,
		}, zap.NewNop()),
	}
}

// warn 은 로거가 없을 때도 안전하게 남긴다. 조회 경로는 로거 없이도 동작해야 하고,
// 테스트가 로깅 설정 없이 Store 를 만든다.
func (s *Store) warn(ctx context.Context, msg string, fields ...zap.Field) {
	if s == nil || s.logger == nil {
		return
	}
	s.warn(ctx, msg, fields...)
}

// Put 은 push 시점에 내린 판정을 적재한다.
func (s *Store) Put(repoPath, sha string, sig Verification) {
	if s == nil || sha == "" {
		return
	}
	s.cache.Set(repoPath+":"+sha, sig, storeTTL)
}

// Annotate 는 커밋 목록에 붙일 판정을 돌려준다.
//
// 캐시에 있으면 push 때 내린 판정을 그대로 쓴다. 없으면 앵커 없이 git 에게 물어
// 서명의 존재 여부까지만 확인한다. 앵커는 push 요청과 함께 오는 것이라 조회 시점에는
// 손에 없고, 그래서 서명이 있는 커밋은 unverified 가 아니라 key-unavailable 이 된다.
// 확인하지 못한 것을 틀렸다고 표시하면 사용자는 멀쩡한 자기 키를 의심하게 된다.
//
// 판독에 실패해도 오류를 올리지 않는다. 배지를 그리지 못하는 것은 화면의 부가 정보가
// 비는 일이고, 그것 때문에 커밋 목록 자체가 실패하면 손해가 훨씬 크다.
func (s *Store) Annotate(ctx context.Context, repoPath string, shas []string) map[string]Verification {
	result := make(map[string]Verification, len(shas))
	if s == nil || len(shas) == 0 {
		return result
	}

	missing := make([]string, 0, len(shas))
	for _, sha := range shas {
		if sig, ok := s.cache.Get(repoPath + ":" + sha); ok {
			result[sha] = sig
			continue
		}
		missing = append(missing, sha)
	}
	if len(missing) == 0 {
		return result
	}

	// 서명의 존재는 커밋 헤더에서 본다. %G? 는 검증 도구가 없으면 서명이 있는 커밋도
	// N 으로 답하기 때문에, 그것으로 미서명을 판단하면 배지가 거짓말을 한다.
	signed, err := signedSHAs(ctx, repoPath, missing)
	if err != nil {
		// 삼키면 "서명 없음" 과 "읽지 못함" 이 운영에서 구분되지 않는다. 배지가 사라진
		// 원인을 찾을 단서가 이 로그뿐이다.
		s.warn(ctx, "서명 헤더 판독 실패 — 해당 커밋은 미서명으로 표시된다",
			zap.String("event", "signature_header_read_failed"),
			zap.String("repo_path", repoPath),
			zap.Int("commit_count", len(missing)),
			zap.Error(err))
	}

	// 키 식별자는 판독으로만 얻을 수 있다. 실패해도 판정은 만든다 — 키 이름이 비는 것과
	// 판정을 못 내리는 것은 다르다.
	read, readErr := ruleguard.ReadCommitSignatures(ctx, repoPath, "", missing)
	if readErr != nil {
		s.warn(ctx, "서명 판독 실패 — 키 식별자 없이 판정한다",
			zap.String("event", "signature_read_failed"),
			zap.String("repo_path", repoPath),
			zap.Int("commit_count", len(missing)),
			zap.Error(readErr))
	}
	keyBySHA := make(map[string]string, len(read))
	for _, sig := range read {
		keyBySHA[sig.SHA] = sig.KeyID
	}

	now := time.Now().UTC()
	for _, sha := range missing {
		result[sha] = s.fromUnanchored(repoPath, sha, signed[sha], keyBySHA[sha], now)
	}
	return result
}

// fromUnanchored 는 앵커 없는 판독을 화면 계약으로 옮긴다.
func (s *Store) fromUnanchored(repoPath, sha string, signed bool, keyID string, now time.Time) Verification {
	if !signed {
		// 미서명은 시간이 지나도 바뀌지 않으므로 캐시에 남긴다.
		unsigned := Verification{
			State:      StateUnsigned,
			SignerType: SignerUser,
			Reason:     ReasonUnsigned,
			VerifiedAt: &now,
		}
		s.Put(repoPath, sha, unsigned)
		return unsigned
	}

	// 이 판정은 앵커가 없어서 나온 것이라 캐시에 남기지 않는다. 남기면 이후 push 가
	// 내린 진짜 판정을 만료될 때까지 가린다.
	return Verification{
		State:      StateKeyUnavailable,
		SignerType: SignerUser,
		Signer:     &Signer{KeyID: keyID},
		Reason:     ReasonAnchorUnavailable,
		VerifiedAt: &now,
	}
}

// defaultStore 는 프로세스 전체가 공유하는 판정 캐시다.
//
// push 가 적재한 판정을 조회가 읽어야 하므로 하나여야 한다. 핸들러마다 따로 만들면
// push 에서 내린 진짜 판정이 조회 경로에 영원히 보이지 않는다.
var defaultStore = NewStore(nil)

// Default 는 공유 판정 캐시를 돌려준다.
func Default() *Store { return defaultStore }

// SetDefaultLogger 는 공유 캐시가 쓸 로거를 붙인다. 핸들러가 만들어질 때 한 번
// 호출한다. 캐시는 프로세스 하나를 공유하므로 로거도 하나면 된다.
func SetDefaultLogger(logger *logging.Logger) { defaultStore.logger = logger }
