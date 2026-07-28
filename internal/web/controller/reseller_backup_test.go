package controller

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

func TestResellerBackupScope(t *testing.T) {
	setupCtrlDB(t)
	db := database.GetDB()
	db.Create(&model.Inbound{Id: 1, Protocol: model.VLESS, Remark: "A"})
	db.Create(&model.Inbound{Id: 2, Protocol: model.VLESS, Remark: "B"})

	link := func(email string, inboundID int) {
		rec := &model.ClientRecord{Email: email, UUID: email}
		if err := db.Create(rec).Error; err != nil {
			t.Fatalf("create %s: %v", email, err)
		}
		if err := db.Create(&model.ClientInbound{ClientId: rec.Id, InboundId: inboundID}).Error; err != nil {
			t.Fatalf("link %s: %v", email, err)
		}
	}
	link("mine", 1)
	link("other", 2)

	reseller := &model.User{Id: 50, Username: "res", Role: model.RoleReseller, AllowedInbounds: "1"}
	ctrl := &ResellerController{}

	w := httptest.NewRecorder()
	c := ctxForUser(reseller)
	c.Request = httptest.NewRequest(http.MethodGet, "/panel/api/reseller/backup", nil)
	c.Writer = w
	ctrl.backup(c)

	if w.Code != http.StatusOK {
		t.Fatalf("backup status = %d, body = %s", w.Code, w.Body.String())
	}
	var resp struct {
		Success bool                           `json:"success"`
		Obj     []service.ClientCreatePayload `json:"obj"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode backup: %v", err)
	}
	if len(resp.Obj) != 1 || resp.Obj[0].Client.Email != "mine" {
		t.Fatalf("backup obj = %+v, want only mine", resp.Obj)
	}

	admin := &model.User{Id: 1, Username: "admin", Role: model.RoleSuperAdmin}
	w2 := httptest.NewRecorder()
	adminCtx := ctxForUser(admin)
	adminCtx.Request = httptest.NewRequest(http.MethodGet, "/panel/api/reseller/backup", nil)
	adminCtx.Writer = w2
	ctrl.backup(adminCtx)
	if w2.Code != http.StatusForbidden {
		t.Errorf("super_admin backup status = %d, want 403", w2.Code)
	}
}

func TestResellerRestoreStripsForeignInbounds(t *testing.T) {
	setupCtrlDB(t)
	db := database.GetDB()
	db.Create(&model.Inbound{Id: 1, Protocol: model.VLESS, Remark: "A", Enable: true, Port: 443})

	reseller := &model.User{Id: 50, Username: "res", Role: model.RoleReseller, AllowedInbounds: "1"}
	ctrl := &ResellerController{}

	payload := []service.ClientCreatePayload{
		{Client: model.Client{Email: "new@x", ID: "u1"}, InboundIds: []int{1, 99}},
	}
	raw, _ := json.Marshal(payload)
	body, _ := json.Marshal(map[string]string{"data": string(raw)})

	w := httptest.NewRecorder()
	c := ctxForUser(reseller)
	c.Request = httptest.NewRequest(http.MethodPost, "/panel/api/reseller/restore", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Writer = w
	ctrl.restore(c)

	if w.Code != http.StatusOK {
		t.Fatalf("restore status = %d, body = %s", w.Code, w.Body.String())
	}
	var rec model.ClientRecord
	if err := db.Where("email = ?", "new@x").First(&rec).Error; err != nil {
		t.Fatalf("restored client missing: %v", err)
	}
	var links int64
	db.Model(&model.ClientInbound{}).Where("client_id = ? AND inbound_id = ?", rec.Id, 1).Count(&links)
	if links != 1 {
		t.Errorf("expected link to inbound 1, got %d", links)
	}
	db.Model(&model.ClientInbound{}).Where("client_id = ? AND inbound_id = ?", rec.Id, 99).Count(&links)
	if links != 0 {
		t.Error("inbound 99 must not be attached after restore")
	}
}
