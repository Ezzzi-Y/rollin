package mail

import (
	"embed"
	"fmt"
	"html"
	"regexp"
	"strings"
	"time"

	"rollin-backend/internal/model"
)

// templateFiles carries the built-in default template bodies (templates/). They are
// compiled into the binary so Docker images need no extra content volume; a
// customization stored in mail_template always takes precedence over these.
// offer.html is sent as text/html: keep every style inline (plus the small responsive
// block in <head>) so it remains usable in common mailbox clients.
//
//go:embed templates/*
var templateFiles embed.FS

// Variable whitelists per template type (04 §5.13: 类型化变量白名单). The OFFER list is
// contract-fixed; the INVITE lists mirror the InvitePayload plus the two link/site
// variables the worker computes. Any {{var}} outside the list is rejected on PUT and
// rendered as empty (with a warning) at send time.
var (
	offerVariables = []string{
		"candidateName", "activityTitle", "offerUrl", "expiresAt", "siteName",
	}
	inviteVariables = []string{
		"inviteeName", "inviteeEmail", "activityTitle", "inviteUrl", "expiresAt", "siteName", "role",
	}
)

// templateVariables returns the whitelist of one template type.
func templateVariables(templateType string) ([]string, error) {
	switch templateType {
	case model.TemplateOffer:
		return offerVariables, nil
	case model.TemplateInviteOwner, model.TemplateInviteAdmin:
		return inviteVariables, nil
	default:
		return nil, fmt.Errorf("unknown template type %q", templateType)
	}
}

// templateVarPattern matches {{var}} placeholders (spaces around the name tolerated).
var templateVarPattern = regexp.MustCompile(`\{\{\s*([A-Za-z][A-Za-z0-9_]*)\s*\}\}`)

// illegalVariables lists every variable of subject+body that is outside the template
// type's whitelist — PUT /mail-templates turns this into VALIDATION_ERROR (04 §5.13).
func illegalVariables(templateType string, subject, body string) []string {
	allowed, err := templateVariables(templateType)
	if err != nil {
		return []string{templateType}
	}
	whitelist := make(map[string]bool, len(allowed))
	for _, name := range allowed {
		whitelist[name] = true
	}
	seen := map[string]bool{}
	var out []string
	for _, tpl := range []string{subject, body} {
		for _, match := range templateVarPattern.FindAllStringSubmatch(tpl, -1) {
			name := match[1]
			if !whitelist[name] && !seen[name] {
				seen[name] = true
				out = append(out, "{{"+name+"}}")
			}
		}
	}
	return out
}

// sanitizeValue makes one substitution safe for both the SMTP headers and the mail body:
// CR/LF are stripped (header/line injection — a candidate named over multiple lines must
// never be able to forge header or body structure) and the rest is HTML-escaped
// (04 §5.13: 内容做 HTML/头注入转义; escaping over-decodes nothing in a text mail but
// makes the same renderer safe for future HTML parts).
func sanitizeValue(value string) string {
	replacer := strings.NewReplacer("\r", " ", "\n", " ", "\t", " ")
	return html.EscapeString(replacer.Replace(value))
}

// renderTemplate substitutes {{var}} placeholders. Values are sanitized
// (sanitizeValue); names outside the whitelist render as the empty string and are
// reported through warn (04 §5.13: 未声明变量渲染为空并告警). Text outside placeholders
// is emitted verbatim — authors may use literal line breaks.
func renderTemplate(tpl string, allowed []string, vars map[string]string, warn func(name string)) string {
	whitelist := make(map[string]bool, len(allowed))
	for _, name := range allowed {
		whitelist[name] = true
	}
	return templateVarPattern.ReplaceAllStringFunc(tpl, func(match string) string {
		name := strings.TrimSpace(match[2 : len(match)-2])
		if !whitelist[name] {
			if warn != nil {
				warn(name)
			}
			return ""
		}
		return sanitizeValue(vars[name])
	})
}

// mailZone is the display timezone for times shown inside mail bodies. It is pinned
// to UTC+8 (北京时间) as a FixedZone so the rendered text does not depend on the
// container/host TZ setting.
var mailZone = time.FixedZone("UTC+8", 8*3600)

// expiresAtText formats the deadline shown inside mail bodies: UTC+8, human readable.
func expiresAtText(t time.Time) string {
	return t.In(mailZone).Format("2006-01-02 15:04:05 UTC+8")
}

// roleDisplay maps the OWNER/ADMIN enum to the Chinese noun used in invitation mail.
func roleDisplay(role string) string {
	if role == model.MemberRoleOwner {
		return "负责人"
	}
	return "管理员"
}

// defaultTemplate returns the built-in subject/body for a template type. Bodies are
// read from the embedded files under templates/; subjects stay here because they are
// single lines. These are the effective templates until an activity (OFFER) or the
// platform (INVITE_*) customizes them; they never carry activity-specific information,
// so they are safe to use for any activity (P3-4: 禁止模板内容包含其他活动信息 —
// activity context enters ONLY through the whitelisted variables).
func defaultTemplate(templateType string) (subject, body string) {
	switch templateType {
	case model.TemplateOffer:
		return "{{siteName}}｜录取通知", loadTemplateFile("offer.html")
	case model.TemplateInviteOwner:
		return "{{siteName}}｜活动负责人邀请", loadTemplateFile("invite_owner.txt")
	case model.TemplateInviteAdmin:
		return "{{siteName}}｜活动管理员邀请", loadTemplateFile("invite_admin.txt")
	default:
		return "", ""
	}
}

// loadTemplateFile reads one embedded default template body. Line endings are
// normalized to CRLF — the convention BuildMessage assumes for SMTP DATA and which the
// former in-code literals used — so the rendered body does not depend on how the source
// tree was checked out. Surrounding whitespace is trimmed to match how UpdateTemplate
// stores customized templates.
func loadTemplateFile(name string) string {
	raw, err := templateFiles.ReadFile("templates/" + name)
	if err != nil {
		// The //go:embed directive compiles the files into the binary, so a miss can
		// only be a name mismatch between this switch and the directive — a build-time
		// bug, not a runtime condition.
		panic("mail: embedded template missing: " + err.Error())
	}
	s := strings.ReplaceAll(string(raw), "\r\n", "\n")
	s = strings.ReplaceAll(s, "\n", "\r\n")
	return strings.TrimSpace(s)
}
