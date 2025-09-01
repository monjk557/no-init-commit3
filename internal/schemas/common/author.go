package common

import "time"

// AuthorInfo represents commit author/committer information
type AuthorInfo struct {
	Name  string    `json:"name"`
	Email string    `json:"email"`
	Date  time.Time `json:"date"`
}

// CommitAuthor represents author/committer information for API requests
// This is a simplified version without Date for request payloads
type CommitAuthor struct {
	Name  string `json:"name" binding:"required"`
	Email string `json:"email" binding:"required"`
}

// ToAuthorInfo converts CommitAuthor to AuthorInfo with current time
func (ca *CommitAuthor) ToAuthorInfo() AuthorInfo {
	return AuthorInfo{
		Name:  ca.Name,
		Email: ca.Email,
		Date:  time.Now(),
	}
}

// ToAuthorInfoWithDate converts CommitAuthor to AuthorInfo with specified date
func (ca *CommitAuthor) ToAuthorInfoWithDate(date time.Time) AuthorInfo {
	return AuthorInfo{
		Name:  ca.Name,
		Email: ca.Email,
		Date:  date,
	}
}
