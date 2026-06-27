package storage

import "testing"

func TestNormalizeRemoteField_StripsDoubleQuotes(t *testing.T) {
	// Reproduces the exact config that caused OSS 400 BadRequest on the server:
	// the bucket name was wrapped in double quotes, so rclone sent literal
	// quote characters to OSS as part of the bucket name.
	input := `[oss]
type = s3
provider = Alibaba
env_auth = false
access_key_id = AKID
secret_access_key = AKSECRET
endpoint = oss-cn-shenzhen.aliyuncs.com
acl = private
bucket = nasbkuptest

[oss-crypt]
type = crypt
remote = oss:"nasbkuptest"
filename_encryption = off
directory_name_encryption = false
password = obscured1
password2 = obscured2
`
	got := normalizeRemoteField(input, "oss-crypt")
	want := `remote = oss:nasbkuptest`
	if !contains(got, want) {
		t.Fatalf("normalizeRemoteField did not strip quotes.\nwant line: %s\ngot:\n%s", want, got)
	}
	// Make sure the old quoted form is gone.
	if contains(got, `oss:"nasbkuptest"`) {
		t.Fatalf("quoted remote still present after normalize:\n%s", got)
	}
}

func TestNormalizeRemoteField_StripsSingleQuotes(t *testing.T) {
	input := `[oss-crypt]
type = crypt
remote = oss:'mybucket'
`
	got := normalizeRemoteField(input, "oss-crypt")
	if !contains(got, `remote = oss:mybucket`) {
		t.Fatalf("single quotes not stripped:\n%s", got)
	}
}

func TestNormalizeRemoteField_NoQuotes_Unchanged(t *testing.T) {
	input := `[oss-crypt]
type = crypt
remote = oss:mybucket
`
	got := normalizeRemoteField(input, "oss-crypt")
	if got != input {
		t.Fatalf("input without quotes should be unchanged.\ngot:\n%s", got)
	}
}

func TestNormalizeRemoteField_SectionAbsent_Unchanged(t *testing.T) {
	input := `[oss]
type = s3
`
	got := normalizeRemoteField(input, "oss-crypt")
	if got != input {
		t.Fatalf("input without the section should be unchanged.\ngot:\n%s", got)
	}
}

func TestStripStorageClass(t *testing.T) {
	input := `[oss]
type = s3
provider = Alibaba
storage_class = Archive
bucket = mybucket

[oss-crypt]
type = crypt
`
	got := stripStorageClass(input)
	if contains(got, "storage_class") {
		t.Fatalf("storage_class not stripped:\n%s", got)
	}
	// The crypt section must be untouched.
	if !contains(got, "[oss-crypt]") {
		t.Fatalf("crypt section lost:\n%s", got)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
