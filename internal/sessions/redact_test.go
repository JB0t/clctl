package sessions

import (
	"strings"
	"testing"
)

func TestRedact(t *testing.T) {
	for _, test := range []struct {
		name, input, want string
		gone              []string // must not survive
	}{
		{
			name:  "private key block",
			input: "key:\n-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQ\nAAAA\n-----END OPENSSH PRIVATE KEY-----\ndone",
			want:  "key:\n[REDACTED:private-key]\ndone",
		},
		{
			name:  "private key cut off",
			input: "-----BEGIN RSA PRIVATE KEY-----\nMIIEpAIBAAKCAQEA",
			want:  "[REDACTED:private-key]",
		},
		{
			name:  "authorization header",
			input: `curl -H "Authorization: Bearer abc.def-123" https://x`,
			want:  `curl -H "Authorization: [REDACTED:authorization]" https://x`,
		},
		{
			name:  "authorization in JSON",
			input: `{"Authorization": "Basic dXNlcjpwYXNz"}`,
			want:  `{"Authorization": "[REDACTED:authorization]"}`,
		},
		{
			name:  "gitlab private token",
			input: `curl --header "PRIVATE-TOKEN: glpat-AbCdEf123" https://gitlab.com/api/v4`,
			want:  `curl --header "PRIVATE-TOKEN: [REDACTED:private-token]" https://gitlab.com/api/v4`,
		},
		{
			name:  "password assignment",
			input: "export DB_PASSWORD=hunter2 && run",
			want:  "export DB_PASSWORD=[REDACTED:secret] && run",
		},
		{
			name:  "yaml secret",
			input: "  client_secret: s3cr3t-value\n  passwd: 'x y'",
			want:  "  client_secret: [REDACTED:secret]\n  passwd: '[REDACTED:secret]'",
		},
		{
			name:  "quoted JSON value",
			input: `{"api_key": "k 1 2", "token": ""}`,
			want:  `{"api_key": "[REDACTED:secret]", "token": ""}`,
		},
		{
			name:  "api key and token in a query string",
			input: "GET /v1?api_key=AKIA1234&token=zzz",
			want:  "GET /v1?api_key=[REDACTED:secret]&token=[REDACTED:secret]",
		},
		{
			name:  "secret flag",
			input: `mysql --password hunter2 db; tool --api-key=k123; x --token "a b"`,
			want:  `mysql --password [REDACTED:secret] db; tool --api-key=[REDACTED:secret]; x --token "[REDACTED:secret]"`,
		},
		{
			name:  "variable references stay",
			input: `password=$DB_PASS token: {{ vault_token }} --token "$TOKEN"`,
			want:  `password=$DB_PASS token: {{ vault_token }} --token "$TOKEN"`,
		},
		{
			name:  "url userinfo",
			input: "git clone https://oauth2:glpat-xyz@gitlab.com/example-org/svc-mp.git",
			want:  "git clone https://[REDACTED:userinfo]@gitlab.com/example-org/svc-mp.git",
		},
		{
			name:  "jwt",
			input: "token eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U ok",
			want:  "token [REDACTED:jwt] ok",
		},
		{
			name:  "hex run",
			input: "sha256 ecbadb99091a3f46e31f5f934b068b1864eafa7995211b39eaddf76996045fe5 ok",
			want:  "sha256 [REDACTED:hex] ok",
		},
		{
			name:  "base64 run",
			input: "aws wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY1 here",
			want:  "aws [REDACTED:base64] here",
		},
		{
			name:  "paths, ids and short hashes stay",
			input: "/home/alex/work/repos/clctl/internal/llm github.com/JB0t/clctl/internal/sessions TestAnsiblePlayWithoutTasksAndRoles 6b23be32-5a40-4e63-90d6-03346769a26b commit 4b16d30",
			want:  "/home/alex/work/repos/clctl/internal/llm github.com/JB0t/clctl/internal/sessions TestAnsiblePlayWithoutTasksAndRoles 6b23be32-5a40-4e63-90d6-03346769a26b commit 4b16d30",
		},
		{
			name:  "ssh remote stays",
			input: "origin git@gitlab.com:example-org/svc-mp.git",
			want:  "origin git@gitlab.com:example-org/svc-mp.git",
		},
	} {
		got := Redact(test.input)
		if got != test.want {
			t.Errorf("%s:\n got %q\nwant %q", test.name, got, test.want)
		}
	}
}

func TestRedactIsIdempotent(t *testing.T) {
	input := "Authorization: Bearer abc\npassword=x\nhttps://u:p@h/\n" + strings.Repeat("aB3", 12)
	once := Redact(input)
	if twice := Redact(once); twice != once {
		t.Fatalf("second pass changed the text:\n%q\n%q", once, twice)
	}
}
