package bot

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

func (b *Bot) handleAdminNodes(ctx context.Context, settings Settings, chatID int64, command, arg string) {
	if b.nodes == nil {
		b.reply(ctx, settings, chatID, "Node status is unavailable.", nil)
		return
	}
	if command == "/node" {
		id, err := strconv.ParseInt(strings.TrimSpace(arg), 10, 64)
		if err != nil || id <= 0 {
			b.reply(ctx, settings, chatID, "Usage: /node &lt;id&gt;", nil)
			return
		}
		node, err := b.nodes.Get(ctx, id)
		if err != nil {
			b.reply(ctx, settings, chatID, "Could not load this node. Check its ID or try again.", nil)
			return
		}
		b.reply(ctx, settings, chatID, fmt.Sprintf("🖥 <b>%s</b> (#%d)\nStatus: %s\nAgent: %s (%s)\nXray: %s (%s)\nCPU: %.1f%% · RAM: %.1f%%\nConfiguration revision: %d/%d",
			escape(node.Name), node.ID, escape(node.Status), escape(node.AgentStatus), escape(node.AgentVersion), escape(node.XrayStatus), escape(node.XrayVersion), node.CPUPercent, node.MemoryPercent, node.AppliedRevision, node.DesiredRevision), nil)
		return
	}
	nodes, err := b.nodes.List(ctx)
	if err != nil {
		b.reply(ctx, settings, chatID, "Could not load nodes. Please try again.", nil)
		return
	}
	if len(nodes) == 0 {
		b.reply(ctx, settings, chatID, "No nodes configured.", nil)
		return
	}
	lines := []string{"<b>AntiMage nodes</b>"}
	for i, node := range nodes {
		if i == 20 {
			lines = append(lines, fmt.Sprintf("… and %d more nodes", len(nodes)-i))
			break
		}
		lines = append(lines, fmt.Sprintf("<code>%d</code> %s · %s / %s", node.ID, escape(node.Name), escape(node.Status), escape(node.AgentStatus)))
	}
	lines = append(lines, "Use /node &lt;id&gt; for live metrics.")
	b.reply(ctx, settings, chatID, strings.Join(lines, "\n"), nil)
}
