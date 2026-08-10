package agent

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/pem"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"nodepanel/shared/proto"
)

// mkLine builds a valid "type base64 [comment]" line for a freshly generated
// key, so the tests never depend on a hard-coded secret.
func mkLine(t *testing.T, suffix string) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	sp, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("new pubkey: %v", err)
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sp)))
	if suffix != "" {
		line += " " + suffix
	}
	return line
}

func TestParseSSHKeyLine_Valid(t *testing.T) {
	line := mkLine(t, "test-comment")
	canonical, comment, ok := parseSSHKeyLine(line)
	if !ok {
		t.Fatalf("valid line rejected")
	}
	if comment != "test-comment" {
		t.Errorf("comment = %q, want test-comment", comment)
	}
	if !strings.HasPrefix(canonical, "ssh-ed25519 ") {
		t.Errorf("canonical missing type prefix: %q", canonical)
	}
	// canonical must itself be valid and idempotent (re-parsing yields the same
	// canonical form), proving dedup-by-canonical is stable.
	c2, _, ok2 := parseSSHKeyLine(canonical)
	if !ok2 {
		t.Fatalf("canonical form is not itself a valid key")
	}
	if c2 != canonical {
		t.Errorf("canonical not idempotent:\n %s\n %s", canonical, c2)
	}
}

func TestParseSSHKeyLine_OptionsPrefix(t *testing.T) {
	line := `no-pty,command="echo hi",from="1.2.3.4" ` + mkLine(t, "opt-comment")
	canonical, comment, ok := parseSSHKeyLine(line)
	if !ok {
		t.Fatalf("options-prefixed valid key rejected")
	}
	if comment != "opt-comment" {
		t.Errorf("comment = %q, want opt-comment", comment)
	}
	if !strings.HasPrefix(canonical, "ssh-ed25519 ") {
		t.Errorf("canonical should strip options: %q", canonical)
	}
}

func TestParseSSHKeyLine_RSA(t *testing.T) {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("gen rsa: %v", err)
	}
	sp, err := ssh.NewPublicKey(&k.PublicKey)
	if err != nil {
		t.Fatalf("new rsa pubkey: %v", err)
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sp)))
	if canonical, _, ok := parseSSHKeyLine(line); !ok {
		t.Errorf("valid RSA key rejected")
	} else if !strings.HasPrefix(canonical, "ssh-rsa ") {
		t.Errorf("RSA canonical type wrong: %q", canonical)
	}
}

func TestParseSSHKeyLine_Invalid(t *testing.T) {
	cases := map[string]string{
		"empty":          "",
		"comment-only":   "# just a comment",
		"garbage":        "this is definitely not a key",
		"bad-base64":     "ssh-ed25519 !!!not-base64!!!",
		"truncated-wire": "ssh-ed25519 AAAAC3NzaC1", // valid b64, but invalid/truncated wire format
		"options-only":   `command="echo hi"`,
		"wrong-type":     "ssh-imaginary AAAAC3NzaC1lZDI1NTE5AAAAIEhEhA",
	}
	for name, in := range cases {
		in := in
		t.Run(name, func(t *testing.T) {
			if _, _, ok := parseSSHKeyLine(in); ok {
				t.Errorf("invalid input accepted as a key: %q", in)
			}
		})
	}
}

// TestDedupByCanonical proves the same key in two places (e.g. a *.pub file and
// an authorized_keys line, with different comments) collapses to one entry.
func TestDedupByCanonical(t *testing.T) {
	c1, _, _ := parseSSHKeyLine(mkLine(t, "from-pub-file"))
	c2, _, _ := parseSSHKeyLine(mkLine(t, "from-pub-file")) // same key, regenerated fresh
	// two independently generated keys must differ
	if c1 == c2 {
		t.Fatalf("two randomly generated keys collided — test is broken")
	}
	// the SAME key text with a different comment must canonicalize identically
	once := mkLine(t, "")
	twice := strings.TrimSpace(once) + " different-comment"
	a, _, _ := parseSSHKeyLine(once)
	b, _, _ := parseSSHKeyLine(twice)
	if a != b {
		t.Errorf("same key with different comment should dedup:\n %s\n %s", a, b)
	}
}

func TestParseSSHDConfig(t *testing.T) {
	cfg := parseSSHDConfig("port 2222\npubkeyauthentication yes\n" +
		"permitrootlogin prohibit-password\nauthorizedkeysfile .ssh/authorized_keys\n" +
		"allowusers alice bob\n# a comment\n")
	if cfg["port"] != "2222" || cfg["pubkeyauthentication"] != "yes" {
		t.Errorf("parsed cfg wrong: %#v", cfg)
	}
	if cfg["permitrootlogin"] != "prohibit-password" {
		t.Errorf("permitrootlogin wrong: %q", cfg["permitrootlogin"])
	}
	if cfg["allowusers"] != "alice bob" {
		t.Errorf("multi-token value wrong: %q", cfg["allowusers"])
	}
}

func authzKey(t *testing.T, user, identity string) collectedKey {
	t.Helper()
	c, _, _ := parseSSHKeyLine(mkLine(t, identity))
	return collectedKey{
		SSHKey:    proto.SSHKey{PubKey: c, User: user, Identity: identity, Path: "/home/" + user + "/.ssh/authorized_keys"},
		fromAuthz: true,
	}
}

// The decisive filter: a key that lives only in a *.pub file is NOT read by
// sshd (which reads AuthorizedKeysFile), so it must be dropped.
func TestApplySSHDFilter_DropsPubOnly(t *testing.T) {
	c, _, _ := parseSSHKeyLine(mkLine(t, "root@host"))
	in := []collectedKey{{
		SSHKey: proto.SSHKey{PubKey: c, User: "root", Identity: "root@host", Path: "/root/.ssh/id_ed25519.pub"},
	}}
	cfg := map[string]string{"authorizedkeysfile": ".ssh/authorized_keys"}
	if got := applySSHDFilter(in, cfg); len(got) != 0 {
		t.Errorf("*.pub-only key should be dropped, got %d", len(got))
	}
	// nil cfg (sshd unreadable) keeps everything — graceful degradation.
	if got := applySSHDFilter(in, nil); len(got) != 1 {
		t.Errorf("nil cfg should keep all, got %d", len(got))
	}
}

func TestApplySSHDFilter_PermitRootNo(t *testing.T) {
	root := authzKey(t, "root", "root@h")
	cfg := map[string]string{"authorizedkeysfile": ".ssh/authorized_keys", "permitrootlogin": "no"}
	if got := applySSHDFilter([]collectedKey{root}, cfg); len(got) != 0 {
		t.Errorf("root key must be dropped when permitrootlogin=no")
	}
	// prohibit-password still allows root pubkey.
	cfg["permitrootlogin"] = "prohibit-password"
	if got := applySSHDFilter([]collectedKey{root}, cfg); len(got) != 1 {
		t.Errorf("root key kept under prohibit-password, got %d", len(got))
	}
}

func TestApplySSHDFilter_AllowUsers(t *testing.T) {
	alice := authzKey(t, "alice", "alice@h")
	bob := authzKey(t, "bob", "bob@h")
	cfg := map[string]string{"authorizedkeysfile": ".ssh/authorized_keys", "allowusers": "alice"}
	got := applySSHDFilter([]collectedKey{alice, bob}, cfg)
	if len(got) != 1 || got[0].User != "alice" {
		t.Errorf("allowusers=alice should keep only alice, got %#v", got)
	}
}

func TestMergeByIdentity(t *testing.T) {
	// three keys, two share the identity "root@metal".
	a := authzKey(t, "root", "root@metal")
	b := authzKey(t, "root", "root@metal")
	c := authzKey(t, "root", "ubuntu@metal")
	out := mergeByIdentity([]collectedKey{a, b, c})
	if len(out) != 2 {
		t.Fatalf("expected 2 identities after merge, got %d", len(out))
	}
	var merged int
	for _, k := range out {
		if k.Identity == "root@metal" {
			merged = k.Merged
		}
	}
	if merged != 2 {
		t.Errorf("root@metal should report Merged=2, got %d", merged)
	}
}

// TestDerivePubFromPriv proves a private key file (even without a sibling .pub)
// yields its matching canonical public key — the fix for nodes whose private
// keys have no .pub and were previously missed.
func TestDerivePubFromPriv(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "host@x")
	if err != nil {
		t.Fatalf("marshal priv: %v", err)
	}
	data := pem.EncodeToMemory(block)
	canonical, encrypted, ok := derivePubFromPriv(data)
	if !ok {
		t.Fatal("private key not recognized")
	}
	if encrypted {
		t.Fatal("unencrypted key reported as encrypted")
	}
	sp, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("new pub: %v", err)
	}
	want := sp.Type() + " " + base64.StdEncoding.EncodeToString(sp.Marshal())
	if canonical != want {
		t.Errorf("derived pub mismatch:\n got %s\n want %s", canonical, want)
	}
	// non-key garbage is rejected.
	if _, _, ok := derivePubFromPriv([]byte("not a private key at all")); ok {
		t.Error("garbage accepted as a private key")
	}
}
