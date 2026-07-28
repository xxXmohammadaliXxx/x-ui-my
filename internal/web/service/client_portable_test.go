package service

import (
	"path/filepath"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func setupPortableDB(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XUI_DB_FOLDER", dir)
	if err := database.InitDB(filepath.Join(dir, "x-ui.db")); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Cleanup(func() { _ = database.CloseDB() })
}

func linkClient(t *testing.T, email string, inboundID int) {
	t.Helper()
	db := database.GetDB()
	rec := &model.ClientRecord{Email: email, UUID: email}
	if err := db.Create(rec).Error; err != nil {
		t.Fatalf("create client %s: %v", email, err)
	}
	if err := db.Create(&model.ClientInbound{ClientId: rec.Id, InboundId: inboundID}).Error; err != nil {
		t.Fatalf("link %s: %v", email, err)
	}
}

func TestExportForInbounds(t *testing.T) {
	setupPortableDB(t)
	db := database.GetDB()
	db.Create(&model.Inbound{Id: 1, Protocol: model.VLESS, Remark: "A"})
	db.Create(&model.Inbound{Id: 2, Protocol: model.VLESS, Remark: "B"})
	linkClient(t, "alice", 1)
	linkClient(t, "bob", 2)
	linkClient(t, "carol", 1)
	var carol model.ClientRecord
	db.Where("email = ?", "carol").First(&carol)
	db.Create(&model.ClientInbound{ClientId: carol.Id, InboundId: 2})

	svc := ClientService{}
	out, err := svc.ExportForInbounds([]int{1})
	if err != nil {
		t.Fatalf("ExportForInbounds: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 clients on inbound #1, got %d", len(out))
	}
	emails := map[string][]int{}
	for _, item := range out {
		emails[item.Client.Email] = item.InboundIds
	}
	if _, ok := emails["alice"]; !ok {
		t.Error("alice missing from export")
	}
	if _, ok := emails["bob"]; ok {
		t.Error("bob must not appear when exporting inbound #1 only")
	}
	if ids := emails["carol"]; len(ids) != 1 || ids[0] != 1 {
		t.Errorf("carol inboundIds = %v, want [1]", ids)
	}
}

func TestSanitizeImportForInbounds(t *testing.T) {
	items := []ClientCreatePayload{
		{Client: model.Client{Email: "a@x"}, InboundIds: []int{1, 2}},
		{Client: model.Client{Email: "b@x"}, InboundIds: []int{3}},
		{Client: model.Client{Email: "c@x"}, InboundIds: []int{}},
	}
	out := SanitizeImportForInbounds(items, []int{1, 3})
	if len(out) != 2 {
		t.Fatalf("expected 2 items after sanitize, got %d", len(out))
	}
	if out[0].Client.Email != "a@x" || len(out[0].InboundIds) != 1 || out[0].InboundIds[0] != 1 {
		t.Errorf("first item = %+v, want a@x with inbound [1]", out[0])
	}
	if out[1].Client.Email != "b@x" || len(out[1].InboundIds) != 1 || out[1].InboundIds[0] != 3 {
		t.Errorf("second item = %+v, want b@x with inbound [3]", out[1])
	}
}
