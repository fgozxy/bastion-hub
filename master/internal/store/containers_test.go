package store

import (
	"context"
	"strings"
	"testing"

	"nodepanel/shared/proto"
)

func openContainerTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(t.TempDir() + "/panel.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.DB.Close() })
	if _, err := st.DB.Exec(`INSERT INTO nodes(id,name,enrollment_token,status,created_at) VALUES('n1','Node 1','token','online',1)`); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestUpdateContainerScanReplacesNodeSnapshotAndReturnsScannedAt(t *testing.T) {
	st := openContainerTestStore(t)
	ctx := context.Background()
	if err := st.ReplaceNodeContainers(ctx, "n1", []Container{
		{ContainerID: strings.Repeat("a", 64), Name: "app", Image: "repo/app:latest"},
		{ContainerID: strings.Repeat("b", 64), Name: "worker", Image: "repo/worker:latest"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateContainerScan(ctx, "n1", []proto.ContainerScanItem{
		{Name: "app", HasUpdate: 1, Note: "new"},
		{Name: "worker", HasUpdate: 0, Note: "current"},
	}); err != nil {
		t.Fatal(err)
	}

	list, err := st.ListContainers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ScannedAt == 0 || list[1].ScannedAt == 0 {
		t.Fatalf("scan timestamps not returned: %+v", list)
	}

	if err := st.UpdateContainerScan(ctx, "n1", []proto.ContainerScanItem{
		{Name: "app", HasUpdate: 0, Note: "now current"},
	}); err != nil {
		t.Fatal(err)
	}
	list, err = st.ListContainers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byName := make(map[string]Container, len(list))
	for _, c := range list {
		byName[c.Name] = c
	}
	if got := byName["app"]; got.HasUpdate != 0 || got.Note != "now current" || got.ScannedAt == 0 {
		t.Fatalf("app cache = %+v", got)
	}
	if got := byName["worker"]; got.HasUpdate != -1 || got.Note != "" || got.ScannedAt != 0 {
		t.Fatalf("stale worker cache was retained: %+v", got)
	}
}

func TestInvalidateContainerScanByContainerAndNode(t *testing.T) {
	st := openContainerTestStore(t)
	ctx := context.Background()
	appID := strings.Repeat("a", 64)
	if err := st.ReplaceNodeContainers(ctx, "n1", []Container{
		{ContainerID: appID, Name: "app"},
		{ContainerID: strings.Repeat("b", 64), Name: "worker"},
	}); err != nil {
		t.Fatal(err)
	}
	seed := func() {
		t.Helper()
		if err := st.UpdateContainerScan(ctx, "n1", []proto.ContainerScanItem{
			{Name: "app", HasUpdate: 1},
			{Name: "worker", HasUpdate: 1},
		}); err != nil {
			t.Fatal(err)
		}
	}
	count := func() int {
		t.Helper()
		var n int
		if err := st.DB.QueryRowContext(ctx, `SELECT count(*) FROM container_scan WHERE node_id='n1'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	seed()
	if err := st.InvalidateContainerScanContainers(ctx, "n1", []string{appID[:12]}); err != nil {
		t.Fatal(err)
	}
	if got := count(); got != 1 {
		t.Fatalf("cache rows after short-id invalidation = %d, want 1", got)
	}
	var remaining string
	if err := st.DB.QueryRowContext(ctx, `SELECT name FROM container_scan WHERE node_id='n1'`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != "worker" {
		t.Fatalf("remaining cache = %q, want worker", remaining)
	}

	seed()
	if err := st.InvalidateContainerScanContainers(ctx, "n1", []string{"worker"}); err != nil {
		t.Fatal(err)
	}
	if got := count(); got != 1 {
		t.Fatalf("cache rows after name invalidation = %d, want 1", got)
	}
	if err := st.InvalidateContainerScan(ctx, "n1"); err != nil {
		t.Fatal(err)
	}
	if got := count(); got != 0 {
		t.Fatalf("cache rows after node invalidation = %d, want 0", got)
	}
}

func TestReplaceNodeContainersInvalidatesChangedAndRemovedScanRows(t *testing.T) {
	st := openContainerTestStore(t)
	ctx := context.Background()
	initial := []Container{
		{ContainerID: strings.Repeat("a", 64), Name: "image-id-changed", Image: "repo/app:latest", ImageID: "sha256:old"},
		{ContainerID: strings.Repeat("b", 64), Name: "image-ref-changed", Image: "repo/api:v1", ImageID: "sha256:api"},
		{ContainerID: strings.Repeat("c", 64), Name: "stable", Image: "repo/stable:v1", ImageID: "sha256:stable", State: "running"},
		{ContainerID: strings.Repeat("d", 64), Name: "removed", Image: "repo/removed:v1", ImageID: "sha256:removed"},
	}
	if err := st.ReplaceNodeContainers(ctx, "n1", initial); err != nil {
		t.Fatal(err)
	}
	items := make([]proto.ContainerScanItem, 0, len(initial))
	for _, c := range initial {
		items = append(items, proto.ContainerScanItem{Name: c.Name, HasUpdate: 0, Note: "current"})
	}
	items = append(items, proto.ContainerScanItem{Name: "new-after-gap", HasUpdate: 0, Note: "orphaned legacy cache"})
	if err := st.UpdateContainerScan(ctx, "n1", items); err != nil {
		t.Fatal(err)
	}

	if err := st.ReplaceNodeContainers(ctx, "n1", []Container{
		{ContainerID: strings.Repeat("a", 64), Name: "image-id-changed", Image: "repo/app:latest", ImageID: "sha256:new"},
		{ContainerID: strings.Repeat("b", 64), Name: "image-ref-changed", Image: "repo/api:v2", ImageID: "sha256:api"},
		{ContainerID: strings.Repeat("c", 64), Name: "stable", Image: "repo/stable:v1", ImageID: "sha256:stable", State: "exited"},
		{ContainerID: strings.Repeat("e", 64), Name: "new-after-gap", Image: "repo/new:v1", ImageID: "sha256:new"},
	}); err != nil {
		t.Fatal(err)
	}

	rows, err := st.DB.QueryContext(ctx, `SELECT name FROM container_scan WHERE node_id='n1' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0] != "stable" {
		t.Fatalf("remaining scan rows = %v, want only stable", names)
	}
}

func TestListContainersExcludesOrphansAndDeleteNodeCleansInventory(t *testing.T) {
	st := openContainerTestStore(t)
	ctx := context.Background()
	if err := st.ReplaceNodeContainers(ctx, "n1", []Container{{
		ContainerID: strings.Repeat("a", 64), Name: "current", Image: "repo/current:latest",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceNodeContainers(ctx, "deleted-node", []Container{{
		ContainerID: strings.Repeat("b", 64), Name: "orphan", Image: "repo/orphan:latest",
	}}); err != nil {
		t.Fatal(err)
	}

	list, err := st.ListContainers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Name != "current" {
		t.Fatalf("visible containers = %+v, want only current node inventory", list)
	}

	if err := st.UpdateContainerScan(ctx, "n1", []proto.ContainerScanItem{{Name: "current", HasUpdate: 1}}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetContainerName(ctx, "n1", "current", "Current"); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteNode(ctx, "n1"); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"containers", "container_scan", "container_names"} {
		var count int
		if err := st.DB.QueryRowContext(ctx, `SELECT count(*) FROM `+table+` WHERE node_id='n1'`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s rows after node delete = %d, want 0", table, count)
		}
	}
}
