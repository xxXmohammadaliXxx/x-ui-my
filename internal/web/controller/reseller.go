// Package controller — reseller self-service endpoints.
package controller

import (
	"encoding/json"
	"net/http"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
	"github.com/mhsanaei/3x-ui/v3/internal/web/session"
	"github.com/mhsanaei/3x-ui/v3/internal/web/websocket"

	"github.com/gin-gonic/gin"
)

// ResellerController exposes the logged-in reseller's own usage/quota snapshot
// under /panel/api/reseller. Accessible to any authenticated user; non-reseller
// roles get an empty/zeroed payload.
type ResellerController struct {
	adminService   service.AdminService
	inboundService service.InboundService
	clientService  service.ClientService
	xrayService    service.XrayService
}

func NewResellerController(g *gin.RouterGroup) *ResellerController {
	a := &ResellerController{}
	g.GET("/me", a.me)
	g.GET("/overview", a.overview)
	g.GET("/backup", a.backup)
	g.POST("/restore", a.restore)
	return a
}

func notifyResellerClientsChanged() {
	websocket.BroadcastInvalidate(websocket.MessageTypeClients)
}

// requireScopedAccount aborts unless the logged-in user holds inbounds.scoped.
func requireScopedAccount(c *gin.Context) (*model.User, bool) {
	u := session.GetLoginUser(c)
	if u == nil {
		c.AbortWithStatus(http.StatusUnauthorized)
		return nil, false
	}
	if !session.IsScoped(c) {
		pureJsonMsg(c, http.StatusForbidden, false, "forbidden: reseller backup is only available to scoped accounts")
		c.Abort()
		return nil, false
	}
	return u, true
}

// overview backs the reseller dashboard: quota, client buckets, the inbounds
// they were given and the clients worth acting on, in one call. A non-reseller
// gets the same shape with empty lists rather than an error, so the page never
// has to special-case who is asking.
func (a *ResellerController) overview(c *gin.Context) {
	u := session.GetLoginUser(c)
	if u == nil {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}
	fresh, err := a.adminService.GetUserByID(u.Id)
	if err != nil {
		jsonMsg(c, "reseller overview", err)
		return
	}
	jsonObj(c, a.adminService.GetResellerOverview(fresh, &a.inboundService), nil)
}

type resellerMe struct {
	Username string `json:"username"`
	Role     string `json:"role"`
	service.ResellerStats
}

func (a *ResellerController) me(c *gin.Context) {
	u := session.GetLoginUser(c)
	if u == nil {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}
	// Reload fresh quota + counters from DB (session copy may predate the field).
	fresh, err := a.adminService.GetUserByID(u.Id)
	if err != nil {
		jsonMsg(c, "reseller me", err)
		return
	}
	out := resellerMe{Username: fresh.Username, Role: fresh.Role}
	if service.IsScopedRole(fresh.Role) {
		out.ResellerStats = a.adminService.GetResellerStats(fresh)
	}
	jsonObj(c, out, nil)
}

// backup exports every client attached to the logged-in reseller's allowed
// inbounds in the same {client, inboundIds} shape used by /clients/import.
func (a *ResellerController) backup(c *gin.Context) {
	u, ok := requireScopedAccount(c)
	if !ok {
		return
	}
	if !session.Can(c, model.PermClientsView) {
		pureJsonMsg(c, http.StatusForbidden, false, "forbidden")
		c.Abort()
		return
	}
	fresh, err := a.adminService.GetUserByID(u.Id)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	items, err := a.clientService.ExportForInbounds(service.AllowedInboundIDs(fresh))
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	jsonObj(c, items, nil)
}

type resellerRestoreRequest struct {
	Data string `json:"data"`
}

// restore imports clients from a backup produced by /reseller/backup. Inbound
// attachments outside the reseller's allowed set are stripped; items with no
// remaining inbound are skipped. Existing emails are never overwritten.
func (a *ResellerController) restore(c *gin.Context) {
	u, ok := requireScopedAccount(c)
	if !ok {
		return
	}
	if !session.Can(c, model.PermClientsManage) {
		pureJsonMsg(c, http.StatusForbidden, false, "forbidden")
		c.Abort()
		return
	}
	var req resellerRestoreRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	var items []service.ClientCreatePayload
	if err := json.Unmarshal([]byte(req.Data), &items); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	fresh, err := a.adminService.GetUserByID(u.Id)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	allowed := service.AllowedInboundIDs(fresh)
	items = service.SanitizeImportForInbounds(items, allowed)
	if len(items) == 0 {
		jsonObj(c, service.BulkCreateResult{}, nil)
		return
	}
	if !a.enforceResellerQuotaFor(c, fresh, len(items)) {
		return
	}
	result, needRestart, err := a.clientService.ImportClients(&a.inboundService, items)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	if result.Created > 0 {
		a.adminService.IncrementClientsCreated(fresh.Id, result.Created)
	}
	jsonObj(c, result, nil)
	if needRestart {
		a.xrayService.SetToNeedRestart()
	}
	notifyResellerClientsChanged()
}

// enforceResellerQuotaFor blocks restore when adding up to n new clients would
// exceed the account's client-count or traffic quota.
func (a *ResellerController) enforceResellerQuotaFor(c *gin.Context, u *model.User, n int) bool {
	if n <= 0 {
		return true
	}
	stats := a.adminService.GetResellerStats(u)
	if stats.ClientQuota > 0 && stats.CurrentClients+n > stats.ClientQuota {
		jsonMsg(c, I18nWeb(c, "pages.reseller.errClientQuota"), nil)
		c.Abort()
		return false
	}
	if stats.TrafficQuotaGB > 0 && stats.TrafficUsedBytes >= stats.TrafficQuotaGB*bytesPerGB {
		jsonMsg(c, I18nWeb(c, "pages.reseller.errTrafficQuota"), nil)
		c.Abort()
		return false
	}
	return true
}
