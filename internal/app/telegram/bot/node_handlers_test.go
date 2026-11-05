package bot

import (
	"context"
	"strings"
	"testing"
)

type fakeNodes struct{ getID int64 }

func (f *fakeNodes) List(context.Context) ([]NodeView, error) {
	return []NodeView{{ID: 7, Name: "node <one>", Status: "connected", AgentStatus: "healthy"}}, nil
}
func (f *fakeNodes) Get(_ context.Context, id int64) (NodeView, error) {
	f.getID = id
	return NodeView{ID: id, Name: "node <one>", Status: "connected", AgentStatus: "healthy", CPUPercent: 15.5}, nil
}

func TestAdminNodeStatusRequiresPrivateAuthorizedChat(t *testing.T) {
	b, calls, settings := newTestBot(t, newFakeUsers())
	nodes := &fakeNodes{}
	b.nodes = nodes
	ctx := context.Background()
	b.handleMessage(ctx, settings, &Message{From: &User{ID: 321}, Chat: Chat{ID: 321, Type: "private"}, Text: "/nodes"})
	if len(*calls) != 0 {
		t.Fatal("customer received admin node status")
	}
	b.handleMessage(ctx, settings, &Message{From: &User{ID: 100}, Chat: Chat{ID: 100, Type: "private"}, Text: "/nodes"})
	call, ok := lastCall(*calls, "sendMessage")
	if !ok || !strings.Contains(call.body["text"].(string), "node &lt;one&gt;") {
		t.Fatalf("node list missing or not escaped: %+v", *calls)
	}
	b.handleMessage(ctx, settings, &Message{From: &User{ID: 100}, Chat: Chat{ID: 100, Type: "private"}, Text: "/node 7"})
	if nodes.getID != 7 {
		t.Fatalf("inspected node id = %d", nodes.getID)
	}
	b.handleCallback(ctx, settings, &CallbackQuery{ID: "nodes-menu", From: &User{ID: 100}, Message: &Message{Chat: Chat{ID: 100, Type: "private"}}, Data: "menu:nodes"})
	if call, ok := lastCall(*calls, "sendMessage"); !ok || !strings.Contains(call.body["text"].(string), "AntiMage nodes") {
		t.Fatal("admin node menu did not open")
	}
}
