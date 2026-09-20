package mail

import (
	"strings"
	"testing"
)

func TestRenderTemplateWhitelistEscapingAndUnknownVars(t *testing.T) {
	allowed, err := templateVariables("OFFER")
	if err != nil {
		t.Fatal(err)
	}
	vars := map[string]string{
		"candidateName": "张三 <管理员>\r\n第二行",
		"activityTitle": "技术部招新",
		"offerUrl":      "https://t.example.edu.cn/o/tok",
		"expiresAt":     expiresAtText(expiresAtFixture),
		"siteName":      "Rollin",
	}
	body := renderTemplate("{{candidateName}}\r\n{{activityTitle}}\r\n{{offerUrl}}\r\n{{expiresAt}}\r\n{{siteName}}\r\n{{bogusVar}}", allowed, vars, nil)
	lines := strings.Split(body, "\r\n")
	if lines[0] != "张三 &lt;管理员&gt;  第二行" {
		t.Fatalf("value must be HTML-escaped and CR/LF-stripped, got %q", lines[0])
	}
	if lines[1] != "技术部招新" || lines[2] != "https://t.example.edu.cn/o/tok" || lines[4] != "Rollin" {
		t.Fatalf("whitelisted variables must substitute: %q", body)
	}
	if lines[5] != "" {
		t.Fatalf("undeclared variables must render empty (04 §5.13), got %q", lines[5])
	}
	if !strings.Contains(lines[3], "2026-09-22 12:00:00 UTC") {
		t.Fatalf("expiresAt format unexpected: %q", lines[3])
	}
}

var expiresAtFixture = mustTime("2026-09-22T12:00:00Z")

func TestIllegalVariablesDetected(t *testing.T) {
	if got := illegalVariables("OFFER", "{{activityTitle}}｜录取通知", "{{candidateName}}: {{offerUrl}} {{password}} {{otherActivityTitle}}"); len(got) != 2 {
		t.Fatalf("expected exactly the 2 illegal variables, got %v", got)
	}
	if got := illegalVariables("OFFER", "{{activityTitle}}", "{{candidateName}} {{offerUrl}} {{expiresAt}} {{siteName}}"); len(got) != 0 {
		t.Fatalf("full whitelist must pass, got %v", got)
	}
	if got := illegalVariables("INVITE_ADMIN", "{{inviteeName}}", "{{inviteUrl}} {{role}} {{siteName}}"); len(got) != 0 {
		t.Fatalf("invite whitelist must pass, got %v", got)
	}
}

func TestDefaultTemplatesOnlyCarryWhitelistedVariables(t *testing.T) {
	for _, templateType := range []string{"OFFER", "INVITE_OWNER", "INVITE_ADMIN"} {
		subject, body := defaultTemplate(templateType)
		if got := illegalVariables(templateType, subject, body); len(got) != 0 {
			t.Fatalf("%s default template uses illegal vars: %v", templateType, got)
		}
	}
}

func TestRoleDisplay(t *testing.T) {
	if roleDisplay("OWNER") != "负责人" || roleDisplay("ADMIN") != "管理员" {
		t.Fatalf("role display mapping broken")
	}
}
