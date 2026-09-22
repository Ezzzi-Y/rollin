package mail

import (
	"fmt"
	"html"
	"regexp"
	"strings"
	"time"

	"rollin-backend/internal/model"
)

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

// defaultTemplate returns the built-in subject/body for a template type. These are the
// effective templates until an activity (OFFER) or the platform (INVITE_*) customizes
// them; they never carry activity-specific information, so they are safe to use for any
// activity (P3-4: 禁止模板内容包含其他活动信息 — activity context enters ONLY through
// the whitelisted variables).
func defaultTemplate(templateType string) (subject, body string) {
	switch templateType {
	case model.TemplateOffer:
		return "{{siteName}}｜录取通知", defaultOfferHTML
	case model.TemplateInviteOwner:
		return "{{siteName}}｜活动负责人邀请",
			"{{inviteeName}}，你好：\r\n\r\n你被邀请担任活动「{{activityTitle}}」的负责人。" +
				"请在 {{expiresAt}} 前打开以下链接设置密码并激活账户：\r\n\r\n{{inviteUrl}}\r\n\r\n{{siteName}}"
	case model.TemplateInviteAdmin:
		return "{{siteName}}｜活动管理员邀请",
			"{{inviteeName}}，你好：\r\n\r\n你被邀请担任活动「{{activityTitle}}」的管理员。" +
				"请在 {{expiresAt}} 前打开以下链接设置密码并激活账户：\r\n\r\n{{inviteUrl}}\r\n\r\n{{siteName}}"
	default:
		return "", ""
	}
}

// defaultOfferHTML is sent as text/html for OFFER tasks. Keep every style inline or
// in the small responsive block so it remains usable in common mailbox clients.
const defaultOfferHTML = `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="x-apple-disable-message-reformatting">
<style>@media screen and (max-width:620px){.wrap{width:100%!important;border-radius:0!important}.pad{padding-left:22px!important;padding-right:22px!important}.button{display:block!important;width:auto!important}}</style>
</head>
<body style="margin:0;padding:0;background:#f2f3f8;color:#182033;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI','PingFang SC','Microsoft YaHei',Arial,sans-serif;">
<div style="display:none;max-height:0;overflow:hidden;opacity:0;">恭喜你通过 {{siteName}} 的面试，请查看并确认专属 Offer。</div>
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="background:#f2f3f8;"><tr><td align="center" style="padding:32px 12px 42px;">
<table role="presentation" class="wrap" width="600" cellpadding="0" cellspacing="0" border="0" style="width:100%;max-width:600px;background:#fff;border-radius:18px;overflow:hidden;box-shadow:0 12px 36px rgba(31,37,70,.10);">
<tr><td style="height:6px;background:#5146b8;font-size:0;line-height:0;">&nbsp;</td></tr>
<tr><td class="pad" style="padding:32px 44px 30px;background:#24214f;background:linear-gradient(135deg,#24214f 0%,#4a4297 100%);">
<img src="https://image.ezzi.asia/2026/9/e61b8fe7d9394752b1ce940c5650b903.png" width="160" alt="{{siteName}}" style="display:block;width:160px;height:auto;max-width:100%;border:0;">
<p style="margin:34px 0 10px;font-size:12px;line-height:18px;letter-spacing:1.8px;color:#cbc7ff;font-weight:700;">INTERVIEW RESULT · OFFER</p>
<h1 style="margin:0;font-size:30px;line-height:42px;letter-spacing:-.4px;color:#fff;font-weight:700;">恭喜你，面试通过</h1>
<p style="margin:14px 0 0;font-size:15px;line-height:25px;color:#e5e3ff;">{{siteName}} 诚邀你查看并确认本次录取 Offer。</p>
</td></tr>
<tr><td class="pad" style="padding:34px 44px 0;">
<p style="margin:0 0 10px;font-size:16px;line-height:26px;color:#1c2538;">{{candidateName}}，你好：</p>
<p style="margin:0;font-size:15px;line-height:27px;color:#5c6577;">感谢你认真参与本次面试。经过综合评估，我们很高兴地通知你：你已通过 {{siteName}} 的选拔。</p>
</td></tr>
<tr><td class="pad" style="padding:26px 44px 0;">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="background:#f7f6ff;border:1px solid #e7e4ff;border-radius:14px;">
<tr><td style="padding:19px 20px 5px;font-size:12px;line-height:18px;color:#7268c7;font-weight:700;letter-spacing:1px;">录取信息</td></tr>
<tr><td style="padding:0 20px 17px;font-size:20px;line-height:30px;color:#292460;font-weight:700;">{{siteName}}</td></tr>
<tr><td style="padding:0 20px 19px;font-size:13px;line-height:22px;color:#747d90;">确认截止时间<br><strong style="font-size:14px;color:#a1530b;">{{expiresAt}}</strong></td></tr>
</table>
</td></tr>
<tr><td class="pad" align="center" style="padding:30px 44px 8px;">
<!--[if mso]><v:roundrect xmlns:v="urn:schemas-microsoft-com:vml" href="{{offerUrl}}" style="height:52px;v-text-anchor:middle;width:300px;" arcsize="18%" fillcolor="#5146b8" stroke="f"><w:anchorlock/><center style="color:#ffffff;font-family:Arial,sans-serif;font-size:15px;font-weight:bold;">查看并确认我的 Offer</center></v:roundrect><![endif]-->
<!--[if !mso]><!--><a class="button" href="{{offerUrl}}" style="display:inline-block;width:300px;box-sizing:border-box;padding:16px 22px;border-radius:10px;background:#5146b8;color:#fff;text-align:center;text-decoration:none;font-size:15px;line-height:20px;font-weight:700;box-shadow:0 8px 16px rgba(81,70,184,.22);">查看并确认我的 Offer&nbsp; →</a><!--<![endif]-->
</td></tr>
<tr><td class="pad" align="center" style="padding:4px 44px 0;font-size:12px;line-height:20px;color:#9299aa;">请在截止时间前完成确认</td></tr>
<tr><td class="pad" style="padding:28px 44px 0;"><table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0"><tr><td style="border-top:1px solid #eaecf1;font-size:0;line-height:0;">&nbsp;</td></tr></table></td></tr>
<tr><td class="pad" style="padding:22px 44px 0;"><p style="margin:0 0 8px;font-size:13px;line-height:21px;color:#485268;font-weight:700;">温馨提示</p><p style="margin:0;font-size:13px;line-height:23px;color:#7a8394;">此链接与本人绑定，请勿转发。若你同时申请了多个方向，请在确认前仔细核实；确认一个 Offer 后，其他方向的录取资格可能随之失效。</p></td></tr>
<tr><td class="pad" style="padding:26px 44px 36px;"><p style="margin:0;font-size:14px;line-height:23px;color:#485268;">期待与你在 {{siteName}} 相见。</p><p style="margin:8px 0 0;font-size:14px;line-height:23px;color:#485268;font-weight:700;">{{siteName}} 招新组</p></td></tr>
</table>
<table role="presentation" width="600" cellpadding="0" cellspacing="0" border="0" style="width:100%;max-width:600px;"><tr><td align="center" style="padding:18px 20px 0;font-size:11px;line-height:18px;color:#a2a8b6;">本邮件由系统自动发送，请勿直接回复。</td></tr></table>
</td></tr></table>
</body>
</html>`
