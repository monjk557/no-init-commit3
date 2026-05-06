package repositories

import "testing"

func TestParseRepoURL_HTTPS(t *testing.T) {
	cases := []struct {
		in    string
		host  string
		owner string
		repo  string
	}{
		{"https://stage.collabops.ai/co-one/git-executor.git", "stage.collabops.ai", "co-one", "git-executor"},
		{"https://stage.collabops.ai/co-one/git-executor", "stage.collabops.ai", "co-one", "git-executor"},
		{"https://github.com/protocolbuffers/protobuf.git", "github.com", "protocolbuffers", "protobuf"},
		{"http://localhost:8080/x/y", "localhost", "x", "y"},
		// gitlab subgroup → owner 가 "group/subgroup" 으로 합쳐짐
		{"https://gitlab.com/group/sub/repo.git", "gitlab.com", "group/sub", "repo"},
	}
	for _, c := range cases {
		got := ParseRepoURL(c.in)
		if got == nil {
			t.Errorf("%q: got nil, want %+v", c.in, c)
			continue
		}
		if got.Host != c.host || got.Owner != c.owner || got.Repo != c.repo {
			t.Errorf("%q: got %+v, want host=%q owner=%q repo=%q", c.in, got, c.host, c.owner, c.repo)
		}
	}
}

func TestParseRepoURL_SCP(t *testing.T) {
	cases := []struct {
		in    string
		host  string
		owner string
		repo  string
	}{
		{"git@github.com:protocolbuffers/protobuf.git", "github.com", "protocolbuffers", "protobuf"},
		{"git@stage.collabops.ai:co-one/git-executor.git", "stage.collabops.ai", "co-one", "git-executor"},
		{"user@host.example:org/r", "host.example", "org", "r"},
	}
	for _, c := range cases {
		got := ParseRepoURL(c.in)
		if got == nil {
			t.Errorf("%q: got nil, want %+v", c.in, c)
			continue
		}
		if got.Host != c.host || got.Owner != c.owner || got.Repo != c.repo {
			t.Errorf("%q: got %+v", c.in, got)
		}
	}
}

func TestParseRepoURL_SSH(t *testing.T) {
	got := ParseRepoURL("ssh://git@stage.collabops.ai:22/co-one/git-executor.git")
	if got == nil {
		t.Fatal("expected non-nil")
	}
	if got.Host != "stage.collabops.ai" || got.Owner != "co-one" || got.Repo != "git-executor" {
		t.Errorf("got %+v", got)
	}
}

func TestParseRepoURL_RejectsRelative(t *testing.T) {
	for _, s := range []string{"../foo.git", "./foo.git", "../../bar"} {
		if got := ParseRepoURL(s); got != nil {
			t.Errorf("%q: relative URL should return nil, got %+v", s, got)
		}
	}
}

func TestParseRepoURL_RejectsMalformed(t *testing.T) {
	for _, s := range []string{
		"",
		"not a url",
		"https://github.com/onlyowner", // missing repo
		"https://github.com",           // missing path
		"file:///tmp/foo",              // unsupported scheme
		"git@host",                     // no path part
	} {
		if got := ParseRepoURL(s); got != nil {
			t.Errorf("%q: malformed URL should return nil, got %+v", s, got)
		}
	}
}

func TestParseRepoURL_StripsGitSuffix(t *testing.T) {
	for _, s := range []string{
		"https://github.com/x/y.git",
		"git@github.com:x/y.git",
		"https://github.com/x/y.git/",
	} {
		got := ParseRepoURL(s)
		if got == nil {
			t.Errorf("%q: nil", s)
			continue
		}
		if got.Repo != "y" {
			t.Errorf("%q: repo=%q want %q", s, got.Repo, "y")
		}
	}
}
