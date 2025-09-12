// API 요청/응답에서 사용되는 공통 데이터 구조체들
package common

import "time"

// AuthorInfo - 커밋 작성자/실행자 정보 (응답용)
type AuthorInfo struct {
	Name  string    `json:"name"`  // 작성자 이름
	Email string    `json:"email"` // 작성자 이메일
	Date  time.Time `json:"date"`  // 커밋 시간
}

// CommitAuthor - API 요청용 작성자/실행자 정보
// 요청 페이로드용으로 Date 필드가 없는 간단한 버전
type CommitAuthor struct {
	Name  string `json:"name" binding:"required"`  // 작성자 이름 (필수)
	Email string `json:"email" binding:"required"` // 작성자 이메일 (필수)
}

// ToAuthorInfo - CommitAuthor를 현재 시간으로 AuthorInfo로 변환
func (ca *CommitAuthor) ToAuthorInfo() AuthorInfo {
	return AuthorInfo{
		Name:  ca.Name,
		Email: ca.Email,
		Date:  time.Now(),
	}
}

// ToAuthorInfoWithDate - CommitAuthor를 지정된 날짜로 AuthorInfo로 변환
func (ca *CommitAuthor) ToAuthorInfoWithDate(date time.Time) AuthorInfo {
	return AuthorInfo{
		Name:  ca.Name,
		Email: ca.Email,
		Date:  date,
	}
}
