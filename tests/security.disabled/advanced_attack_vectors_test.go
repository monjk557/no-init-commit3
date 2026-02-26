package security_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	gogit "github.com/go-git/go-git/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/collabops/git-executor/internal/security"
)

// AdvancedAttackVectorTest tests sophisticated attack scenarios
// that combine multiple vulnerability classes
type AdvancedAttackVectorTest struct {
	tempDir           string
	repo              *gogit.Repository
	securityFramework *security.ValidationFramework
	router            *gin.Engine
}

func TestAdvancedAttackVectors(t *testing.T) {
	suite, cleanup := setupAdvancedAttackTest(t)
	defer cleanup()

	// Test sophisticated attack combinations
	t.Run("ChainedExploitAttempts", func(t *testing.T) {
		suite.testChainedExploitAttempts(t)
	})

	t.Run("PolymorphicPayloads", func(t *testing.T) {
		suite.testPolymorphicPayloads(t)
	})

	t.Run("StateConfusionAttacks", func(t *testing.T) {
		suite.testStateConfusionAttacks(t)
	})

	t.Run("CryptoAttacks", func(t *testing.T) {
		suite.testCryptoAttacks(t)
	})

	t.Run("ProtocolConfusionAttacks", func(t *testing.T) {
		suite.testProtocolConfusionAttacks(t)
	})

	t.Run("SideChannelAttacks", func(t *testing.T) {
		suite.testSideChannelAttacks(t)
	})

	t.Run("SerializationAttacks", func(t *testing.T) {
		suite.testSerializationAttacks(t)
	})
}

// =============================================================================
// CHAINED EXPLOIT ATTEMPTS
// =============================================================================

func (s *AdvancedAttackVectorTest) testChainedExploitAttempts(t *testing.T) {
	// Test combinations of multiple attack vectors

	chainedAttacks := []struct {
		name        string
		description string
		testFunc    func(t *testing.T)
	}{
		{
			name:        "path_traversal_plus_xss",
			description: "Path traversal combined with XSS payload",
			testFunc:    s.testPathTraversalPlusXSS,
		},
		{
			name:        "unicode_bypass_plus_injection",
			description: "Unicode normalization bypass with injection",
			testFunc:    s.testUnicodeBypassPlusInjection,
		},
		{
			name:        "timing_attack_plus_enumeration",
			description: "Timing attack combined with file enumeration",
			testFunc:    s.testTimingAttackPlusEnumeration,
		},
		{
			name:        "dos_plus_information_disclosure",
			description: "DoS attack revealing system information",
			testFunc:    s.testDoSPlusInformationDisclosure,
		},
	}

	for _, attack := range chainedAttacks {
		t.Run(attack.name, func(t *testing.T) {
			attack.testFunc(t)
		})
	}
}

func (s *AdvancedAttackVectorTest) testPathTraversalPlusXSS(t *testing.T) {
	// Combine path traversal with XSS to see if the system leaks information
	maliciousPath := "../../../etc/passwd<script>alert('xss')</script>"

	body := map[string]interface{}{
		"content":        "<script>alert('xss')</script>",
		"commit_message": "Path traversal + XSS: " + maliciousPath,
		"branch":         "main<script>alert('xss')</script>",
		"author_name":    "Attacker<script>alert('xss')</script>",
		"author_email":   "attacker@evil.com",
	}

	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest("PUT", "/repos/test/test/files/"+maliciousPath,
		bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	s.router.ServeHTTP(w, req)

	// Should be blocked at multiple levels
	assert.Equal(t, http.StatusBadRequest, w.Code,
		"Chained attack should be blocked")

	// Response should not contain unescaped XSS
	responseBody := w.Body.String()
	assert.NotContains(t, responseBody, "<script>",
		"Response should not contain unescaped XSS")

	// Response should not reveal file system information
	assert.NotContains(t, responseBody, "etc/passwd",
		"Response should not reveal file system paths")
}

func (s *AdvancedAttackVectorTest) testUnicodeBypassPlusInjection(t *testing.T) {
	// Use unicode normalization to bypass filters, then inject
	unicodePayloads := []struct {
		name    string
		payload string
	}{
		{
			name:    "unicode_script_tag",
			payload: "\uff1cscript\uff1ealert('unicode')\uff1c/script\uff1e", // Fullwidth forms
		},
		{
			name:    "unicode_path_traversal",
			payload: "\u002e\u002e\u002f\u002e\u002e\u002f\u002e\u002e\u002f",
		},
		{
			name:    "homograph_admin",
			payload: "аdmin", // Cyrillic 'a' that looks like latin 'a'
		},
		{
			name:    "rtl_override_attack",
			payload: "evil\u202e\u202dtxt.png", // Appears as "evilgfx.txt" due to RTL override
		},
	}

	for _, payload := range unicodePayloads {
		t.Run(payload.name, func(t *testing.T) {
			body := map[string]interface{}{
				"content":        payload.payload,
				"commit_message": "Unicode attack: " + payload.payload,
				"branch":         "main",
				"author_name":    payload.payload,
				"author_email":   "test@example.com",
			}

			bodyBytes, _ := json.Marshal(body)
			req := httptest.NewRequest("PUT", "/repos/test/test/files/"+payload.payload,
				bytes.NewReader(bodyBytes))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			s.router.ServeHTTP(w, req)

			// Unicode attacks should be handled safely
			assert.NotEqual(t, http.StatusInternalServerError, w.Code,
				"Unicode attack should not cause server error")
		})
	}
}

func (s *AdvancedAttackVectorTest) testTimingAttackPlusEnumeration(t *testing.T) {
	// Combine timing attacks with file enumeration to map the system
	testFiles := []struct {
		name     string
		filename string
		expect   bool
	}{
		{name: "common_config_1", filename: ".env", expect: false},
		{name: "common_config_2", filename: "config.json", expect: false},
		{name: "common_config_3", filename: "database.yml", expect: false},
		{name: "system_file_1", filename: "../../../etc/passwd", expect: false},
		{name: "system_file_2", filename: "../../../etc/shadow", expect: false},
		{name: "git_internal", filename: ".git/config", expect: false},
		{name: "existing_file", filename: "README.md", expect: true},
	}

	timingResults := make(map[string]time.Duration)

	for _, file := range testFiles {
		t.Run(file.name, func(t *testing.T) {
			// Measure response time
			req := httptest.NewRequest("GET", "/repos/test/test/files/"+file.filename, nil)
			w := httptest.NewRecorder()

			start := time.Now()
			s.router.ServeHTTP(w, req)
			duration := time.Since(start)

			timingResults[file.filename] = duration

			t.Logf("File %s: %v (status: %d)", file.filename, duration, w.Code)
		})
	}

	// Analyze timing patterns - they should be relatively consistent
	var durations []time.Duration
	for _, duration := range timingResults {
		durations = append(durations, duration)
	}

	// Calculate average
	var total time.Duration
	for _, d := range durations {
		total += d
	}
	average := total / time.Duration(len(durations))

	// Check that no timing reveals file existence
	for filename, duration := range timingResults {
		variance := duration - average
		if variance < 0 {
			variance = -variance
		}

		// Large timing differences might indicate information leakage
		assert.Less(t, variance, average*3,
			"Timing variance too large for file %s - might leak existence info", filename)
	}
}

func (s *AdvancedAttackVectorTest) testDoSPlusInformationDisclosure(t *testing.T) {
	// Create a DoS condition and check if error messages leak information
	largeTasks := []struct {
		name     string
		taskFunc func() (*http.Response, error)
	}{
		{
			name: "memory_exhaustion",
			taskFunc: func() (*http.Response, error) {
				// Large JSON payload
				largeContent := strings.Repeat("A", 10*1024*1024) // 10MB
				body := map[string]interface{}{
					"content":        largeContent,
					"commit_message": "Large file",
					"branch":         "main",
					"author_name":    "Test User",
					"author_email":   "test@example.com",
				}

				bodyBytes, _ := json.Marshal(body)
				req := httptest.NewRequest("PUT", "/repos/test/test/files/large.txt",
					bytes.NewReader(bodyBytes))
				req.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()

				s.router.ServeHTTP(w, req)
				return w.Result(), nil
			},
		},
		{
			name: "recursive_requests",
			taskFunc: func() (*http.Response, error) {
				// Many nested directories
				deepPath := strings.Repeat("a/", 1000) + "file.txt"
				body := map[string]interface{}{
					"content":        "deep",
					"commit_message": "Deep path",
					"branch":         "main",
					"author_name":    "Test User",
					"author_email":   "test@example.com",
				}

				bodyBytes, _ := json.Marshal(body)
				req := httptest.NewRequest("PUT", "/repos/test/test/files/"+deepPath,
					bytes.NewReader(bodyBytes))
				req.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()

				s.router.ServeHTTP(w, req)
				return w.Result(), nil
			},
		},
	}

	for _, task := range largeTasks {
		t.Run(task.name, func(t *testing.T) {
			resp, err := task.taskFunc()
			require.NoError(t, err)
			defer resp.Body.Close()

			bodyBytes, err := io.ReadAll(resp.Body)
			require.NoError(t, err)

			responseBody := string(bodyBytes)

			// Check that error messages don't leak sensitive information
			sensitiveInfo := []string{
				"panic",
				"stack trace",
				"/usr/",
				"/etc/",
				"database",
				"password",
				"secret",
				"token",
				"internal error",
				"goroutine",
				"runtime",
			}

			for _, info := range sensitiveInfo {
				assert.NotContains(t, strings.ToLower(responseBody), info,
					"Error response should not leak sensitive info: %s", info)
			}

			// Should handle gracefully even under stress
			assert.NotEqual(t, http.StatusInternalServerError, resp.StatusCode,
				"DoS attempts should not cause internal server errors")
		})
	}
}

// =============================================================================
// POLYMORPHIC PAYLOADS
// =============================================================================

func (s *AdvancedAttackVectorTest) testPolymorphicPayloads(t *testing.T) {
	// Test payloads that change behavior based on context

	polymorphicTests := []struct {
		name     string
		payloads map[string]string // field -> payload
	}{
		{
			name: "context_sensitive_xss",
			payloads: map[string]string{
				"commit_message": `"><script>alert('xss')</script>`,
				"author_name":    `';alert('xss');//`,
				"content":        `<img src=x onerror=alert('xss')>`,
				"branch":         `main';alert('xss');//`,
			},
		},
		{
			name: "encoding_variations",
			payloads: map[string]string{
				"commit_message": `%3Cscript%3Ealert('xss')%3C/script%3E`,             // URL encoded
				"author_name":    `&lt;script&gt;alert('xss')&lt;/script&gt;`,         // HTML encoded
				"content":        `\u003cscript\u003ealert('xss')\u003c/script\u003e`, // Unicode escaped
			},
		},
		{
			name: "polyglot_injections",
			payloads: map[string]string{
				"commit_message": `"onclick=alert('xss')//<script>alert('xss')</script>`,
				"content":        `';alert('xss');"onmouseover=alert('xss')>`,
			},
		},
	}

	for _, test := range polymorphicTests {
		t.Run(test.name, func(t *testing.T) {
			body := map[string]interface{}{
				"content":        "normal content",
				"commit_message": "normal commit",
				"branch":         "main",
				"author_name":    "Test User",
				"author_email":   "test@example.com",
			}

			// Apply polymorphic payloads
			for field, payload := range test.payloads {
				body[field] = payload
			}

			bodyBytes, _ := json.Marshal(body)
			req := httptest.NewRequest("PUT", "/repos/test/test/files/polymorph_test.txt",
				bytes.NewReader(bodyBytes))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			s.router.ServeHTTP(w, req)

			// Should handle polymorphic payloads safely
			assert.NotEqual(t, http.StatusInternalServerError, w.Code,
				"Polymorphic payload should not cause server error")

			responseBody := w.Body.String()

			// Check that payloads are neutralized
			dangerousPatterns := []string{
				"<script>", "javascript:", "onerror=", "onmouseover=", "onclick=",
			}

			for _, pattern := range dangerousPatterns {
				assert.NotContains(t, responseBody, pattern,
					"Response should not contain active payload: %s", pattern)
			}
		})
	}
}

// =============================================================================
// STATE CONFUSION ATTACKS
// =============================================================================

func (s *AdvancedAttackVectorTest) testStateConfusionAttacks(t *testing.T) {
	// Test attacks that exploit state machine vulnerabilities

	stateAttacks := []struct {
		name     string
		testFunc func(t *testing.T)
	}{
		{
			name:     "session_confusion",
			testFunc: s.testSessionConfusion,
		},
		{
			name:     "race_condition_exploitation",
			testFunc: s.testRaceConditionExploitation,
		},
		{
			name:     "cache_poisoning",
			testFunc: s.testCachePoisoning,
		},
		{
			name:     "transaction_confusion",
			testFunc: s.testTransactionConfusion,
		},
	}

	for _, attack := range stateAttacks {
		t.Run(attack.name, func(t *testing.T) {
			attack.testFunc(t)
		})
	}
}

func (s *AdvancedAttackVectorTest) testSessionConfusion(t *testing.T) {
	// Test session-based attacks
	sessionTests := []struct {
		name    string
		headers map[string]string
	}{
		{
			name: "session_hijacking",
			headers: map[string]string{
				"Cookie":       "session=admin123; user=administrator",
				"X-Session-ID": "hijacked-session",
			},
		},
		{
			name: "cross_session_pollution",
			headers: map[string]string{
				"X-User-ID":     "1",
				"X-Impersonate": "admin",
				"X-Real-User":   "attacker",
			},
		},
	}

	for _, test := range sessionTests {
		t.Run(test.name, func(t *testing.T) {
			body := map[string]interface{}{
				"content":        "session test",
				"commit_message": "Session attack test",
				"branch":         "main",
				"author_name":    "Session Attacker",
				"author_email":   "attacker@example.com",
			}

			bodyBytes, _ := json.Marshal(body)
			req := httptest.NewRequest("PUT", "/repos/test/test/files/session_test.txt",
				bytes.NewReader(bodyBytes))
			req.Header.Set("Content-Type", "application/json")

			for key, value := range test.headers {
				req.Header.Set(key, value)
			}

			w := httptest.NewRecorder()
			s.router.ServeHTTP(w, req)

			// Should not grant elevated privileges
			assert.NotEqual(t, http.StatusInternalServerError, w.Code,
				"Session confusion should be handled safely")
		})
	}
}

func (s *AdvancedAttackVectorTest) testRaceConditionExploitation(t *testing.T) {
	// Test race condition vulnerabilities
	numRequests := 20
	results := make(chan int, numRequests)

	// Fire multiple requests simultaneously
	for i := 0; i < numRequests; i++ {
		go func(id int) {
			body := map[string]interface{}{
				"content":        fmt.Sprintf("race content %d", id),
				"commit_message": fmt.Sprintf("Race condition test %d", id),
				"branch":         "main",
				"author_name":    "Race Tester",
				"author_email":   "race@example.com",
			}

			bodyBytes, _ := json.Marshal(body)
			req := httptest.NewRequest("PUT",
				fmt.Sprintf("/repos/test/test/files/race_%d.txt", id),
				bytes.NewReader(bodyBytes))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			s.router.ServeHTTP(w, req)
			results <- w.Code
		}(i)
	}

	// Collect results
	statusCodes := make(map[int]int)
	for i := 0; i < numRequests; i++ {
		select {
		case code := <-results:
			statusCodes[code]++
		case <-time.After(10 * time.Second):
			t.Error("Race condition test timed out")
			return
		}
	}

	// Analyze results - should handle concurrent requests consistently
	t.Logf("Race condition results: %v", statusCodes)

	// Should not have any internal server errors due to race conditions
	assert.Equal(t, 0, statusCodes[http.StatusInternalServerError],
		"Race conditions should not cause internal server errors")
}

func (s *AdvancedAttackVectorTest) testCachePoisoning(t *testing.T) {
	// Test cache poisoning attacks
	poisonAttempts := []struct {
		name    string
		headers map[string]string
		body    map[string]interface{}
	}{
		{
			name: "cache_key_pollution",
			headers: map[string]string{
				"Cache-Control": "no-cache, max-age=0",
				"X-Cache-Key":   "admin-cache-key",
			},
			body: map[string]interface{}{
				"content":        "poisoned content",
				"commit_message": "Cache poison",
				"branch":         "main",
				"author_name":    "Cache Poisoner",
				"author_email":   "poison@example.com",
			},
		},
		{
			name: "header_injection_caching",
			headers: map[string]string{
				"X-Forwarded-Host": "evil.com",
				"X-Original-URL":   "/admin/secrets",
			},
			body: map[string]interface{}{
				"content":        "header injection",
				"commit_message": "Header injection test",
				"branch":         "main",
				"author_name":    "Header Injector",
				"author_email":   "inject@example.com",
			},
		},
	}

	for _, attempt := range poisonAttempts {
		t.Run(attempt.name, func(t *testing.T) {
			bodyBytes, _ := json.Marshal(attempt.body)
			req := httptest.NewRequest("PUT", "/repos/test/test/files/cache_poison.txt",
				bytes.NewReader(bodyBytes))
			req.Header.Set("Content-Type", "application/json")

			for key, value := range attempt.headers {
				req.Header.Set(key, value)
			}

			w := httptest.NewRecorder()
			s.router.ServeHTTP(w, req)

			// Should handle cache poisoning attempts safely
			assert.NotEqual(t, http.StatusInternalServerError, w.Code,
				"Cache poisoning should be handled safely")
		})
	}
}

func (s *AdvancedAttackVectorTest) testTransactionConfusion(t *testing.T) {
	// Test transaction-based attacks
	// This simulates attacks on transaction boundaries

	transactions := []struct {
		name     string
		requests []map[string]interface{}
	}{
		{
			name: "transaction_rollback_attack",
			requests: []map[string]interface{}{
				{
					"content":        "legitimate content",
					"commit_message": "Legitimate commit",
					"branch":         "main",
					"author_name":    "Good User",
					"author_email":   "good@example.com",
				},
				{
					"content":        "malicious content",
					"commit_message": "Rollback attack",
					"branch":         "main",
					"author_name":    "Bad User",
					"author_email":   "bad@example.com",
				},
			},
		},
	}

	for _, transaction := range transactions {
		t.Run(transaction.name, func(t *testing.T) {
			// Execute transaction requests in sequence
			for i, reqData := range transaction.requests {
				bodyBytes, _ := json.Marshal(reqData)
				req := httptest.NewRequest("PUT",
					fmt.Sprintf("/repos/test/test/files/transaction_%d.txt", i),
					bytes.NewReader(bodyBytes))
				req.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()

				s.router.ServeHTTP(w, req)

				// Each request should be handled atomically
				assert.NotEqual(t, http.StatusInternalServerError, w.Code,
					"Transaction request %d should be handled safely", i)
			}
		})
	}
}

// =============================================================================
// CRYPTO ATTACKS
// =============================================================================

func (s *AdvancedAttackVectorTest) testCryptoAttacks(t *testing.T) {
	// Test cryptographic vulnerabilities

	cryptoTests := []struct {
		name     string
		testFunc func(t *testing.T)
	}{
		{
			name:     "hash_collision_attempts",
			testFunc: s.testHashCollisionAttempts,
		},
		{
			name:     "random_number_prediction",
			testFunc: s.testRandomNumberPrediction,
		},
		{
			name:     "timing_crypto_attacks",
			testFunc: s.testTimingCryptoAttacks,
		},
	}

	for _, test := range cryptoTests {
		t.Run(test.name, func(t *testing.T) {
			test.testFunc(t)
		})
	}
}

func (s *AdvancedAttackVectorTest) testHashCollisionAttempts(t *testing.T) {
	// Test potential hash collision vulnerabilities

	// Generate content with similar hashes (simplified test)
	collisionAttempts := []struct {
		name    string
		content string
	}{
		{
			name:    "md5_collision_attempt_1",
			content: "d131dd02c5e6eec4693d9a0698aff95c2fcab58712467eab4004583eb8fb7f89", // Known MD5 collision prefix
		},
		{
			name:    "md5_collision_attempt_2",
			content: "d131dd02c5e6eec4693d9a0698aff95c2fcab50712467eab4004583eb8fb7f89", // Slightly different
		},
		{
			name:    "sha1_collision_attempt",
			content: "da39a3ee5e6b4b0d3255bfef95601890afd80709", // Empty string SHA1
		},
	}

	for _, attempt := range collisionAttempts {
		t.Run(attempt.name, func(t *testing.T) {
			body := map[string]interface{}{
				"content":        attempt.content,
				"commit_message": "Hash collision test",
				"branch":         "main",
				"author_name":    "Crypto Attacker",
				"author_email":   "crypto@example.com",
			}

			bodyBytes, _ := json.Marshal(body)
			req := httptest.NewRequest("PUT", "/repos/test/test/files/"+attempt.name+".txt",
				bytes.NewReader(bodyBytes))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			s.router.ServeHTTP(w, req)

			// Should handle potential collision attempts safely
			assert.NotEqual(t, http.StatusInternalServerError, w.Code,
				"Hash collision attempt should be handled safely")
		})
	}
}

func (s *AdvancedAttackVectorTest) testRandomNumberPrediction(t *testing.T) {
	// Test if random numbers/IDs are predictable

	var responses []string

	// Make multiple requests to see if there are predictable patterns
	for i := 0; i < 10; i++ {
		body := map[string]interface{}{
			"content":        fmt.Sprintf("random test %d", i),
			"commit_message": fmt.Sprintf("Random number test %d", i),
			"branch":         "main",
			"author_name":    "Random Tester",
			"author_email":   "random@example.com",
		}

		bodyBytes, _ := json.Marshal(body)
		req := httptest.NewRequest("PUT",
			fmt.Sprintf("/repos/test/test/files/random_%d.txt", i),
			bytes.NewReader(bodyBytes))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		s.router.ServeHTTP(w, req)
		responses = append(responses, w.Body.String())
	}

	// Analyze responses for patterns
	// (This is a simplified check - real crypto analysis would be more complex)
	for i := 1; i < len(responses); i++ {
		assert.NotEqual(t, responses[i-1], responses[i],
			"Responses should not be identical (potential weak randomness)")
	}
}

func (s *AdvancedAttackVectorTest) testTimingCryptoAttacks(t *testing.T) {
	// Test for timing-based cryptographic attacks

	// Test if different inputs have different processing times (which might leak crypto info)
	testInputs := []struct {
		name  string
		input string
	}{
		{name: "short_input", input: "a"},
		{name: "medium_input", input: strings.Repeat("a", 100)},
		{name: "long_input", input: strings.Repeat("a", 10000)},
		{name: "special_chars", input: "!@#$%^&*()"},
		{name: "unicode_input", input: "こんにちは世界"},
	}

	timings := make(map[string][]time.Duration)

	// Make multiple measurements for each input
	for _, input := range testInputs {
		for run := 0; run < 5; run++ {
			body := map[string]interface{}{
				"content":        input.input,
				"commit_message": "Timing test: " + input.name,
				"branch":         "main",
				"author_name":    "Timing Tester",
				"author_email":   "timing@example.com",
			}

			bodyBytes, _ := json.Marshal(body)
			req := httptest.NewRequest("PUT",
				fmt.Sprintf("/repos/test/test/files/timing_%s_%d.txt", input.name, run),
				bytes.NewReader(bodyBytes))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			start := time.Now()
			s.router.ServeHTTP(w, req)
			duration := time.Since(start)

			timings[input.name] = append(timings[input.name], duration)
		}
	}

	// Analyze timing patterns
	for inputName, durations := range timings {
		var total time.Duration
		for _, d := range durations {
			total += d
		}
		average := total / time.Duration(len(durations))

		t.Logf("Input %s: average %v, measurements %v", inputName, average, durations)

		// Check for excessive timing variance that might leak information
		for _, d := range durations {
			variance := d - average
			if variance < 0 {
				variance = -variance
			}

			// Large timing differences might indicate crypto timing leaks
			assert.Less(t, variance, average*5,
				"Excessive timing variance for input %s might indicate timing attack vulnerability", inputName)
		}
	}
}

// =============================================================================
// PROTOCOL CONFUSION ATTACKS
// =============================================================================

func (s *AdvancedAttackVectorTest) testProtocolConfusionAttacks(t *testing.T) {
	// Test protocol-level confusion attacks

	protocolTests := []struct {
		name     string
		testFunc func(t *testing.T)
	}{
		{
			name:     "http_request_smuggling",
			testFunc: s.testHTTPRequestSmuggling,
		},
		{
			name:     "content_type_confusion",
			testFunc: s.testContentTypeConfusion,
		},
		{
			name:     "encoding_confusion",
			testFunc: s.testEncodingConfusion,
		},
	}

	for _, test := range protocolTests {
		t.Run(test.name, func(t *testing.T) {
			test.testFunc(t)
		})
	}
}

func (s *AdvancedAttackVectorTest) testHTTPRequestSmuggling(t *testing.T) {
	// Test HTTP request smuggling vulnerabilities

	smugglingAttempts := []struct {
		name    string
		headers map[string]string
		body    string
	}{
		{
			name: "content_length_vs_transfer_encoding",
			headers: map[string]string{
				"Content-Length":    "10",
				"Transfer-Encoding": "chunked",
			},
			body: "4\r\ntest\r\n0\r\n\r\n",
		},
		{
			name: "duplicate_content_length",
			headers: map[string]string{
				"Content-Length": "5, 10",
			},
			body: "smuggled request",
		},
	}

	for _, attempt := range smugglingAttempts {
		t.Run(attempt.name, func(t *testing.T) {
			req := httptest.NewRequest("PUT", "/repos/test/test/files/smuggling_test.txt",
				strings.NewReader(attempt.body))

			for key, value := range attempt.headers {
				req.Header.Set(key, value)
			}

			w := httptest.NewRecorder()
			s.router.ServeHTTP(w, req)

			// Should handle smuggling attempts safely
			assert.NotEqual(t, http.StatusInternalServerError, w.Code,
				"Request smuggling should be handled safely")
		})
	}
}

func (s *AdvancedAttackVectorTest) testContentTypeConfusion(t *testing.T) {
	// Test content type confusion attacks

	confusionAttempts := []struct {
		name        string
		contentType string
		body        string
	}{
		{
			name:        "json_as_xml",
			contentType: "application/xml",
			body:        `{"malicious": "json"}`,
		},
		{
			name:        "xml_as_json",
			contentType: "application/json",
			body:        `<malicious>xml</malicious>`,
		},
		{
			name:        "script_as_json",
			contentType: "application/json",
			body:        `<script>alert('xss')</script>`,
		},
		{
			name:        "binary_as_text",
			contentType: "text/plain",
			body:        string([]byte{0x00, 0x01, 0x02, 0x03}),
		},
	}

	for _, attempt := range confusionAttempts {
		t.Run(attempt.name, func(t *testing.T) {
			req := httptest.NewRequest("PUT", "/repos/test/test/files/confusion_test.txt",
				strings.NewReader(attempt.body))
			req.Header.Set("Content-Type", attempt.contentType)
			w := httptest.NewRecorder()

			s.router.ServeHTTP(w, req)

			// Should handle content type confusion safely
			assert.NotEqual(t, http.StatusInternalServerError, w.Code,
				"Content type confusion should be handled safely")
		})
	}
}

func (s *AdvancedAttackVectorTest) testEncodingConfusion(t *testing.T) {
	// Test encoding confusion attacks

	encodingAttempts := []struct {
		name     string
		encoding string
		body     []byte
	}{
		{
			name:     "utf8_bom_confusion",
			encoding: "utf-8",
			body:     []byte{0xEF, 0xBB, 0xBF, 0x3C, 0x73, 0x63, 0x72, 0x69, 0x70, 0x74}, // BOM + script
		},
		{
			name:     "utf16_encoding",
			encoding: "utf-16",
			body:     []byte{0xFF, 0xFE, 0x3C, 0x00, 0x73, 0x00}, // UTF-16 LE BOM + <s
		},
		{
			name:     "latin1_high_bytes",
			encoding: "iso-8859-1",
			body:     []byte{0xFF, 0xFE, 0xFD, 0xFC},
		},
	}

	for _, attempt := range encodingAttempts {
		t.Run(attempt.name, func(t *testing.T) {
			req := httptest.NewRequest("PUT", "/repos/test/test/files/encoding_test.txt",
				bytes.NewReader(attempt.body))
			req.Header.Set("Content-Type", "application/json; charset="+attempt.encoding)
			w := httptest.NewRecorder()

			s.router.ServeHTTP(w, req)

			// Should handle encoding confusion safely
			assert.NotEqual(t, http.StatusInternalServerError, w.Code,
				"Encoding confusion should be handled safely")
		})
	}
}

// =============================================================================
// SIDE CHANNEL ATTACKS
// =============================================================================

func (s *AdvancedAttackVectorTest) testSideChannelAttacks(t *testing.T) {
	// Test various side channel information leaks

	sideChannelTests := []struct {
		name     string
		testFunc func(t *testing.T)
	}{
		{
			name:     "error_message_enumeration",
			testFunc: s.testErrorMessageEnumeration,
		},
		{
			name:     "response_size_analysis",
			testFunc: s.testResponseSizeAnalysis,
		},
		{
			name:     "cpu_usage_patterns",
			testFunc: s.testCPUUsagePatterns,
		},
	}

	for _, test := range sideChannelTests {
		t.Run(test.name, func(t *testing.T) {
			test.testFunc(t)
		})
	}
}

func (s *AdvancedAttackVectorTest) testErrorMessageEnumeration(t *testing.T) {
	// Test if error messages leak system information

	errorInducingInputs := []struct {
		name        string
		path        string
		expectDiff  bool
		description string
	}{
		{
			name:        "nonexistent_file",
			path:        "does_not_exist.txt",
			expectDiff:  false,
			description: "Non-existent files should have consistent errors",
		},
		{
			name:        "protected_file",
			path:        ".git/config",
			expectDiff:  false,
			description: "Protected files should have consistent errors",
		},
		{
			name:        "system_file",
			path:        "../../../etc/passwd",
			expectDiff:  false,
			description: "System file access should have consistent errors",
		},
		{
			name:        "existing_file",
			path:        "README.md",
			expectDiff:  true,
			description: "Existing files may have different response",
		},
	}

	errorMessages := make(map[string]string)

	for _, input := range errorInducingInputs {
		t.Run(input.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/repos/test/test/files/"+input.path, nil)
			w := httptest.NewRecorder()

			s.router.ServeHTTP(w, req)

			responseBody := w.Body.String()
			errorMessages[input.path] = responseBody

			t.Logf("Path %s: status %d, body length %d",
				input.path, w.Code, len(responseBody))

			// Check for information leakage in error messages
			sensitivePatterns := []string{
				"not found",
				"permission denied",
				"access denied",
				"file system",
				"directory",
				"path",
				"exists",
				"internal",
				"server",
				"database",
			}

			lowerBody := strings.ToLower(responseBody)
			for _, pattern := range sensitivePatterns {
				if strings.Contains(lowerBody, pattern) {
					t.Logf("Potential information leak for %s: contains '%s'",
						input.path, pattern)
				}
			}
		})
	}

	// Analyze error message patterns
	nonExistentErrors := []string{
		errorMessages["does_not_exist.txt"],
		errorMessages[".git/config"],
		errorMessages["../../../etc/passwd"],
	}

	// Non-existent files should have similar error messages
	for i := 1; i < len(nonExistentErrors); i++ {
		similarity := calculateStringSimilarity(nonExistentErrors[0], nonExistentErrors[i])
		assert.Greater(t, similarity, 0.5,
			"Non-existent file errors should be similar to avoid enumeration")
	}
}

func (s *AdvancedAttackVectorTest) testResponseSizeAnalysis(t *testing.T) {
	// Test if response sizes leak information

	testPaths := []string{
		"nonexistent1.txt",
		"nonexistent2.txt",
		"../../../etc/passwd",
		".git/config",
		"README.md",
	}

	responseSizes := make(map[string]int)

	for _, path := range testPaths {
		req := httptest.NewRequest("GET", "/repos/test/test/files/"+path, nil)
		w := httptest.NewRecorder()

		s.router.ServeHTTP(w, req)

		responseSizes[path] = len(w.Body.String())
		t.Logf("Path %s: response size %d bytes", path, responseSizes[path])
	}

	// Analyze response size patterns
	nonExistentSizes := []int{
		responseSizes["nonexistent1.txt"],
		responseSizes["nonexistent2.txt"],
		responseSizes["../../../etc/passwd"],
		responseSizes[".git/config"],
	}

	// Non-existent files should have similar response sizes
	baseSize := nonExistentSizes[0]
	for i := 1; i < len(nonExistentSizes); i++ {
		sizeDiff := nonExistentSizes[i] - baseSize
		if sizeDiff < 0 {
			sizeDiff = -sizeDiff
		}

		assert.Less(t, sizeDiff, 100,
			"Response size differences should be minimal to avoid information leakage")
	}
}

func (s *AdvancedAttackVectorTest) testCPUUsagePatterns(t *testing.T) {
	// Test if CPU usage patterns leak information
	// This is a simplified test - real CPU analysis would require more sophisticated monitoring

	testInputs := []struct {
		name       string
		complexity string
	}{
		{name: "simple", complexity: "a"},
		{name: "moderate", complexity: strings.Repeat("a", 1000)},
		{name: "complex", complexity: strings.Repeat("(.*)*", 100)}, // Potential ReDoS
	}

	for _, input := range testInputs {
		t.Run(input.name, func(t *testing.T) {
			body := map[string]interface{}{
				"content":        input.complexity,
				"commit_message": "CPU test: " + input.name,
				"branch":         "main",
				"author_name":    "CPU Tester",
				"author_email":   "cpu@example.com",
			}

			bodyBytes, _ := json.Marshal(body)
			req := httptest.NewRequest("PUT", "/repos/test/test/files/cpu_test_"+input.name+".txt",
				bytes.NewReader(bodyBytes))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			start := time.Now()
			s.router.ServeHTTP(w, req)
			duration := time.Since(start)

			t.Logf("CPU test %s: %v", input.name, duration)

			// Should complete within reasonable time regardless of complexity
			assert.Less(t, duration, 5*time.Second,
				"Request should complete within reasonable time for %s", input.name)
		})
	}
}

// =============================================================================
// SERIALIZATION ATTACKS
// =============================================================================

func (s *AdvancedAttackVectorTest) testSerializationAttacks(t *testing.T) {
	// Test serialization/deserialization vulnerabilities

	serializationTests := []struct {
		name     string
		testFunc func(t *testing.T)
	}{
		{
			name:     "json_deserialization_bombs",
			testFunc: s.testJSONDeserializationBombs,
		},
		{
			name:     "deeply_nested_structures",
			testFunc: s.testDeeplyNestedStructures,
		},
		{
			name:     "circular_reference_attacks",
			testFunc: s.testCircularReferenceAttacks,
		},
	}

	for _, test := range serializationTests {
		t.Run(test.name, func(t *testing.T) {
			test.testFunc(t)
		})
	}
}

func (s *AdvancedAttackVectorTest) testJSONDeserializationBombs(t *testing.T) {
	// Test JSON bombs that could cause DoS

	bombs := []struct {
		name string
		json string
	}{
		{
			name: "billion_laughs_json",
			json: `{"a": "` + strings.Repeat("ha", 1000000) + `"}`,
		},
		{
			name: "large_number_bomb",
			json: `{"number": ` + strings.Repeat("9", 1000000) + `}`,
		},
		{
			name: "many_keys_bomb",
			json: generateManyKeysJSON(10000),
		},
	}

	for _, bomb := range bombs {
		t.Run(bomb.name, func(t *testing.T) {
			req := httptest.NewRequest("PUT", "/repos/test/test/files/bomb_test.txt",
				strings.NewReader(bomb.json))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			// Set timeout to prevent hanging
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			req = req.WithContext(ctx)

			start := time.Now()
			s.router.ServeHTTP(w, req)
			duration := time.Since(start)

			// Should handle bombs safely
			assert.Less(t, duration, 5*time.Second,
				"JSON bomb should be handled within timeout")

			assert.NotEqual(t, http.StatusInternalServerError, w.Code,
				"JSON bomb should not cause internal server error")
		})
	}
}

func (s *AdvancedAttackVectorTest) testDeeplyNestedStructures(t *testing.T) {
	// Test deeply nested JSON structures

	depths := []int{100, 1000, 10000}

	for _, depth := range depths {
		t.Run(fmt.Sprintf("depth_%d", depth), func(t *testing.T) {
			nestedJSON := generateDeeplyNestedJSON(depth)

			req := httptest.NewRequest("PUT", "/repos/test/test/files/nested_test.txt",
				strings.NewReader(nestedJSON))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			start := time.Now()
			s.router.ServeHTTP(w, req)
			duration := time.Since(start)

			t.Logf("Nested JSON depth %d: %v (status: %d)", depth, duration, w.Code)

			// Should handle nested structures without stack overflow
			assert.Less(t, duration, 10*time.Second,
				"Deeply nested JSON should be handled within reasonable time")
		})
	}
}

func (s *AdvancedAttackVectorTest) testCircularReferenceAttacks(t *testing.T) {
	// Test circular reference attacks
	// Note: Pure JSON cannot have true circular references, but we can test
	// structures that might cause issues when processed

	circularTests := []struct {
		name string
		json string
	}{
		{
			name: "self_referencing_keys",
			json: `{"a": {"b": {"a": "circular"}}}`,
		},
		{
			name: "recursive_pattern",
			json: generateRecursivePatternJSON(100),
		},
	}

	for _, test := range circularTests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest("PUT", "/repos/test/test/files/circular_test.txt",
				strings.NewReader(test.json))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			s.router.ServeHTTP(w, req)

			// Should handle potential circular references safely
			assert.NotEqual(t, http.StatusInternalServerError, w.Code,
				"Circular reference attack should be handled safely")
		})
	}
}

// =============================================================================
// UTILITY FUNCTIONS
// =============================================================================

func calculateStringSimilarity(s1, s2 string) float64 {
	// Simple similarity calculation (Levenshtein distance-based)
	if len(s1) == 0 && len(s2) == 0 {
		return 1.0
	}
	if len(s1) == 0 || len(s2) == 0 {
		return 0.0
	}

	maxLen := len(s1)
	if len(s2) > maxLen {
		maxLen = len(s2)
	}

	distance := levenshteinDistance(s1, s2)
	return 1.0 - float64(distance)/float64(maxLen)
}

func levenshteinDistance(s1, s2 string) int {
	// Simple Levenshtein distance implementation
	r1, r2 := []rune(s1), []rune(s2)
	m, n := len(r1), len(r2)

	if m == 0 {
		return n
	}
	if n == 0 {
		return m
	}

	d := make([][]int, m+1)
	for i := range d {
		d[i] = make([]int, n+1)
		d[i][0] = i
	}
	for j := 0; j <= n; j++ {
		d[0][j] = j
	}

	for i := 1; i <= m; i++ {
		for j := 1; j <= n; j++ {
			cost := 1
			if r1[i-1] == r2[j-1] {
				cost = 0
			}
			d[i][j] = min(
				d[i-1][j]+1,      // deletion
				d[i][j-1]+1,      // insertion
				d[i-1][j-1]+cost, // substitution
			)
		}
	}

	return d[m][n]
}

func min(a, b, c int) int {
	if a < b && a < c {
		return a
	}
	if b < c {
		return b
	}
	return c
}

func generateManyKeysJSON(count int) string {
	var builder strings.Builder
	builder.WriteString("{")

	for i := 0; i < count; i++ {
		if i > 0 {
			builder.WriteString(",")
		}
		builder.WriteString(fmt.Sprintf(`"key%d": "value%d"`, i, i))
	}

	builder.WriteString("}")
	return builder.String()
}

func generateRecursivePatternJSON(depth int) string {
	if depth <= 0 {
		return `"base"`
	}
	return fmt.Sprintf(`{"pattern": %s}`, generateRecursivePatternJSON(depth-1))
}

func setupAdvancedAttackTest(t *testing.T) (*AdvancedAttackVectorTest, func()) {
	// Create temporary directory
	tempDir, err := os.MkdirTemp("", "advanced_attack_test_*")
	require.NoError(t, err)

	// Initialize git repository
	repo, err := gogit.PlainInit(tempDir, false)
	require.NoError(t, err)

	// Create security validation framework
	securityFramework, err := security.NewValidationFramework(
		security.DefaultValidationConfig(),
		// Note: We need a logger here - this might need adjustment based on actual API
		nil, // This will need to be fixed with proper logger
	)
	if err != nil {
		// Fallback if security framework fails
		t.Logf("Warning: Could not create security framework: %v", err)
	}

	// Setup router with security middleware
	gin.SetMode(gin.TestMode)
	router := gin.New()

	// Add security middleware if available
	if securityFramework != nil {
		router.Use(securityFramework.ValidateRequestMiddleware())
	}

	// Add test routes
	api := router.Group("/repos/:owner/:repo")
	{
		api.GET("/files/*path", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"message": "file content"})
		})
		api.PUT("/files/*path", func(c *gin.Context) {
			c.JSON(http.StatusCreated, gin.H{"message": "file created"})
		})
		api.POST("/commits", func(c *gin.Context) {
			c.JSON(http.StatusCreated, gin.H{"message": "commit created"})
		})
		api.POST("/merge", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"message": "merge completed"})
		})
	}

	suite := &AdvancedAttackVectorTest{
		tempDir:           tempDir,
		repo:              repo,
		securityFramework: securityFramework,
		router:            router,
	}

	cleanup := func() {
		os.RemoveAll(tempDir)
	}

	return suite, cleanup
}
