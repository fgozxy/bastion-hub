package settings

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// projectGitignore excludes build artifacts and local data so only source is
// pushed for other servers to clone and build.
const projectGitignore = `node_modules/
web/node_modules/
dist/
build/
master/internal/webassets/dist/
*.tar.gz
*.db
*.db-*
*.log
.env
.tmp/
mobile/nodepanel.keystore
mobile/.keystore-pass
mobile/*.keystore
`

// gitPushProject initializes (if needed) a git repo at dir, commits the current
// source, and pushes to owner/repo on the given branch. The token is used inline
// on the push URL only (never stored in git config) and redacted from the log.
func gitPushProject(ctx context.Context, dir, token, owner, repo, branch string, force bool) (string, error) {
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("项目目录不存在: %s", dir)
	}
	var logb strings.Builder
	run := func(args ...string) error {
		c := exec.CommandContext(ctx, "git", args...)
		c.Dir = dir
		out, err := c.CombinedOutput()
		logb.WriteString("$ git " + strings.Join(args, " ") + "\n" + string(out) + "\n")
		return err
	}

	// ensure a repo exists with the desired default branch
	_ = run("init")
	_ = run("symbolic-ref", "HEAD", "refs/heads/"+branch)

	// write a .gitignore on first push
	gi := filepath.Join(dir, ".gitignore")
	if _, err := os.Stat(gi); err != nil {
		_ = os.WriteFile(gi, []byte(projectGitignore), 0o644)
	}

	// auto-refresh the README's dynamic block (version/date/changes) so the
	// GitHub project description updates on every push without manual edits.
	runOut := func(args ...string) string {
		c := exec.CommandContext(ctx, "git", args...)
		c.Dir = dir
		out, _ := c.CombinedOutput()
		return string(out)
	}
	if err := updateReadmeAuto(dir, runOut); err != nil {
		logb.WriteString("readme auto-update: " + err.Error() + "\n")
	}

	idArgs := []string{"-c", "user.name=NodePanel", "-c", "user.email=nodepanel@local"}
	if err := run(append(idArgs, "add", "-A")...); err != nil {
		return redact(logb.String(), token), fmt.Errorf("git add 失败")
	}
	msg := "NodePanel project upload @ " + time.Now().Format("2006-01-02 15:04:05")
	_ = run(append(idArgs, "commit", "-m", msg)...) // "nothing to commit" is fine

	// push by URL (token inline, not stored); -f optional
	url := fmt.Sprintf("https://x-access-token:%s@github.com/%s/%s.git", token, owner, repo)
	args := []string{"push"}
	if force {
		args = append(args, "-f")
	}
	args = append(args, url, "HEAD:refs/heads/"+branch)
	err := run(args...)
	return redact(logb.String(), token), err
}

func redact(s, token string) string {
	if token == "" {
		return s
	}
	return strings.ReplaceAll(s, token, "***")
}

const (
	readmeAutoBegin     = "<!-- NODEPANEL:AUTO:BEGIN -->"
	readmeAutoEnd       = "<!-- NODEPANEL:AUTO:END -->"
	readmeFeaturesBegin = "<!-- NODEPANEL:FEATURES:BEGIN -->"
	readmeFeaturesEnd   = "<!-- NODEPANEL:FEATURES:END -->"
)

// updateReadmeAuto refreshes two README blocks on every push:
//   - NODEPANEL:AUTO — version, timestamp, this-push change summary, recent commits
//   - NODEPANEL:FEATURES — the feature list, rendered from docs/FEATURES.md
//     (the single source of truth; edit that file to update the README's feature
//     section without touching README itself).
//
// Content outside the markers is untouched. AUTO is inserted after the first
// heading if absent; FEATURES is only written where its markers already are.
func updateReadmeAuto(dir string, gitRun func(...string) string) error {
	version := readAgentVersion(dir)
	now := time.Now().UTC().Format("2006-01-02 15:04:05 UTC")
	diffStat := strings.TrimSpace(lastNonEmptyLine(gitRun("diff", "--stat", "HEAD")))
	recent := strings.TrimSpace(gitRun("log", "--oneline", "-5"))

	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", readmeAutoBegin)
	b.WriteString("> ")
	if now != "" {
		fmt.Fprintf(&b, "📅 **最近更新**:%s", now)
	}
	if version != "" {
		fmt.Fprintf(&b, "  ·  🏷️ **Agent 版本**:%s", version)
	}
	if diffStat != "" {
		fmt.Fprintf(&b, "  ·  📝 **本次变更**:%s", diffStat)
	}
	b.WriteString("\n\n")
	if recent != "" {
		b.WriteString("<details><summary>📦 最近提交</summary>\n\n```text\n")
		b.WriteString(recent)
		b.WriteString("\n```\n\n</details>\n")
	}
	fmt.Fprintf(&b, "%s\n", readmeAutoEnd)
	autoBlock := b.String()

	var featuresBlock string
	if f, err := os.ReadFile(filepath.Join(dir, "docs/FEATURES.md")); err == nil {
		featuresBlock = strings.TrimRight(string(f), "\n")
	}

	readme := filepath.Join(dir, "README.md")
	data, err := os.ReadFile(readme)
	if err != nil {
		return os.WriteFile(readme, []byte("# NodePanel\n\n"+autoBlock+"\n"), 0o644)
	}
	content := string(data)
	updated := replaceMarkerBlock(content, readmeAutoBegin, readmeAutoEnd, autoBlock, true)
	if featuresBlock != "" {
		// Wrap with the markers so they survive the replacement (mirrors how
		// autoBlock is built). Without this, replaceMarkerBlock strips the
		// FEATURES markers on the first sync and the section never re-syncs.
		wrapped := readmeFeaturesBegin + "\n" + featuresBlock + "\n" + readmeFeaturesEnd
		updated = replaceMarkerBlock(updated, readmeFeaturesBegin, readmeFeaturesEnd, wrapped, false)
	}
	if updated == content {
		return nil
	}
	return os.WriteFile(readme, []byte(updated), 0o644)
}

// replaceMarkerBlock replaces the content between begin/end markers with block.
// If markers are absent: inserts after the first heading when insertIfMissing is
// true, otherwise leaves content unchanged.
func replaceMarkerBlock(content, begin, end, block string, insertIfMissing bool) string {
	bi := strings.Index(content, begin)
	ei := strings.Index(content, end)
	if bi >= 0 && ei > bi {
		return content[:bi] + block + content[ei+len(end):]
	}
	if insertIfMissing {
		return insertAfterFirstHeading(content, block)
	}
	return content
}

// readAgentVersion extracts the AgentVersion constant from agent.go.
func readAgentVersion(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, "agent/internal/agent/agent.go"))
	if err != nil {
		return ""
	}
	s := string(data)
	key := `AgentVersion = "`
	i := strings.Index(s, key)
	if i < 0 {
		return ""
	}
	s = s[i+len(key):]
	if j := strings.IndexByte(s, '"'); j >= 0 {
		return s[:j]
	}
	return ""
}

// insertAfterFirstHeading places block right after the first line (so it shows
// under the project title), or at the top if the file is empty.
func insertAfterFirstHeading(content, block string) string {
	nl := strings.IndexByte(content, '\n')
	if nl < 0 {
		return content + "\n\n" + block + "\n"
	}
	return content[:nl+1] + "\n" + block + "\n" + content[nl+1:]
}

// lastNonEmptyLine returns the final non-empty line of s (the
// "N files changed, ..." summary line of git diff --stat).
func lastNonEmptyLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if t := strings.TrimSpace(lines[i]); t != "" {
			return t
		}
	}
	return ""
}
