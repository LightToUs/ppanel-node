package node

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/lighttous/ppanel-node/api/panel"
	"github.com/lighttous/ppanel-node/conf"
)

func TestApplyControlMessageIgnoresDuplicateAndOlderRevision(t *testing.T) {
	controller := &Controller{
		started:          true,
		controlRevisions: make(map[int64]int64),
		controlStates: map[int64]string{
			42: "authorized",
		},
	}
	message := panel.ControlMessage{
		Type:            "authorization_changed",
		UserSubscribeID: 42,
		AuthRevision:    3,
		DesiredState:    "revoked",
	}

	if err := controller.applyControlMessage(context.Background(), message); err != nil {
		t.Fatalf("apply revoke: %v", err)
	}
	if got := controller.controlRevisions[42]; got != 3 {
		t.Fatalf("revision = %d, want 3", got)
	}
	if got := controller.controlStates[42]; got != "revoked" {
		t.Fatalf("state = %q, want revoked", got)
	}

	message.AuthRevision = 2
	message.DesiredState = "authorized"
	if err := controller.applyControlMessage(context.Background(), message); err != nil {
		t.Fatalf("apply older authorization: %v", err)
	}
	if got := controller.controlRevisions[42]; got != 3 {
		t.Fatalf("revision = %d after stale event, want 3", got)
	}
	if got := controller.controlStates[42]; got != "revoked" {
		t.Fatalf("state = %q after stale event, want revoked", got)
	}
}

func TestControlAgentCloseInterruptsBlockedRead(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		conn, err := (&websocket.Upgrader{}).Upgrade(writer, request, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		<-release
	}))
	defer server.Close()
	defer close(release)

	conn, _, err := websocket.DefaultDialer.Dial(
		"ws"+strings.TrimPrefix(server.URL, "http"),
		nil,
	)
	if err != nil {
		t.Fatalf("dial test websocket: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	agent := &controlAgent{
		cancel: cancel,
		done:   make(chan struct{}),
		conn:   conn,
	}
	go func() {
		defer close(agent.done)
		_ = agent.readLoop(ctx, conn)
	}()

	closed := make(chan struct{})
	go func() {
		agent.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("control agent close blocked on websocket read")
	}
}

func TestAuthorizedControlRollsBackRevisionWhenSyncFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		_ *http.Request,
	) {
		http.Error(writer, "panel unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	client, err := panel.NewClientV1(&conf.NodeApiConfig{
		APIHost:   server.URL,
		NodeType:  "hysteria2",
		NodeID:    1,
		SecretKey: "secret",
	})
	if err != nil {
		t.Fatalf("new panel client: %v", err)
	}
	controller := &Controller{
		started:   true,
		apiClient: client,
		controlRevisions: map[int64]int64{
			42: 3,
		},
		controlStates: map[int64]string{
			42: "revoked",
		},
	}

	err = controller.applyControlMessage(context.Background(), panel.ControlMessage{
		UserSubscribeID: 42,
		AuthRevision:    4,
		DesiredState:    "authorized",
	})
	if err == nil {
		t.Fatal("authorized control succeeded while panel was unavailable")
	}
	if controller.controlRevisions[42] != 3 ||
		controller.controlStates[42] != "revoked" {
		t.Fatalf(
			"failed sync committed control state: revision=%d state=%q",
			controller.controlRevisions[42],
			controller.controlStates[42],
		)
	}
}

func TestAuthorizedControlIgnoresUntrackedSubscription(t *testing.T) {
	controller := &Controller{
		started:          true,
		controlRevisions: make(map[int64]int64),
		controlStates:    make(map[int64]string),
	}

	err := controller.applyControlMessage(context.Background(), panel.ControlMessage{
		UserSubscribeID: 42,
		AuthRevision:    1,
		DesiredState:    "authorized",
	})
	if err != nil {
		t.Fatalf("ignore untracked authorization: %v", err)
	}
	if len(controller.controlRevisions) != 0 || len(controller.controlStates) != 0 {
		t.Fatalf(
			"untracked authorization polluted control state: revisions=%v states=%v",
			controller.controlRevisions,
			controller.controlStates,
		)
	}
}

type fakeControlConnection struct {
	messages []panel.ControlMessage
	writes   []interface{}
}

func (c *fakeControlConnection) ReadJSON(value interface{}) error {
	if len(c.messages) == 0 {
		return io.EOF
	}
	target := value.(*panel.ControlMessage)
	*target = c.messages[0]
	c.messages = c.messages[1:]
	return nil
}

func (c *fakeControlConnection) WriteJSON(value interface{}) error {
	c.writes = append(c.writes, value)
	return nil
}

func TestControlAgentRequestsOnlyTrackedSubscriptionSnapshot(t *testing.T) {
	controller := &Controller{
		userList: []panel.UserInfo{{Id: 11}, {Id: 12}},
		controlStates: map[int64]string{
			12: "revoked",
			13: "revoked",
		},
	}
	agent := newControlAgent(nil, []*Controller{controller})
	connection := &fakeControlConnection{
		messages: []panel.ControlMessage{{Type: "ready"}},
	}

	if err := agent.readLoop(context.Background(), connection); err != io.EOF {
		t.Fatalf("read loop error = %v, want EOF", err)
	}
	if len(connection.writes) != 1 {
		t.Fatalf("snapshot requests = %d, want 1", len(connection.writes))
	}
	request := connection.writes[0].(panel.ControlRequest)
	got := make(map[int64]struct{}, len(request.SubscriptionIDs))
	for _, id := range request.SubscriptionIDs {
		got[id] = struct{}{}
	}
	for _, id := range []int64{11, 12, 13} {
		if _, ok := got[id]; !ok {
			t.Fatalf("snapshot request missing subscription %d: %v", id, request.SubscriptionIDs)
		}
	}
}

func TestFilterRevokedUsersProtectsAgainstStaleSnapshot(t *testing.T) {
	controller := &Controller{
		controlStates: map[int64]string{
			2: "revoked",
		},
	}
	users := []panel.UserInfo{
		{Id: 1, Uuid: "active"},
		{Id: 2, Uuid: "stale-revoked"},
		{Id: 3, Uuid: "active-too"},
	}

	filtered := controller.filterRevokedUsers(users)

	want := []panel.UserInfo{
		{Id: 1, Uuid: "active"},
		{Id: 3, Uuid: "active-too"},
	}
	if len(filtered) != len(want) {
		t.Fatalf("filtered users = %v, want %v", filtered, want)
	}
	for i := range want {
		if filtered[i].Id != want[i].Id || filtered[i].Uuid != want[i].Uuid {
			t.Fatalf("filtered users = %v, want %v", filtered, want)
		}
	}
}
