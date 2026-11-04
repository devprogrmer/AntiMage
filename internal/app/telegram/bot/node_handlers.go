package bot

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

func (b *Bot) handleAdminNodes(ctx context.Context, settings Settings, chatID int64, command, arg string) {
	if b.nodes == nil {
		b.reply(ctx, settings, chatID, localText(ctx, "Node status is unavailable.", "وضعیت نودها در دسترس نیست.", "Состояние узлов недоступно."), nil)
		return
	}
	if command == "/node" {
		id, err := strconv.ParseInt(strings.TrimSpace(arg), 10, 64)
		if err != nil || id <= 0 {
			b.reply(ctx, settings, chatID, localText(ctx, "Usage: /node &lt;id&gt;", "روش استفاده: /node &lt;شناسه&gt;", "Использование: /node &lt;ID&gt;"), nil)
			return
		}
		node, err := b.nodes.Get(ctx, id)
		if err != nil {
			b.reply(ctx, settings, chatID, localText(ctx, "Could not load this node. Check its ID or try again.", "نود بارگذاری نشد. شناسه را بررسی کنید یا دوباره تلاش کنید.", "Не удалось загрузить узел. Проверьте ID или попробуйте снова."), nil)
			return
		}
		b.reply(ctx, settings, chatID, fmt.Sprintf(localText(ctx, "🖥 <b>%s</b> (#%d)\nStatus: %s\nAgent: %s (%s)\nXray: %s (%s)\nCPU: %.1f%% · RAM: %.1f%%\nConfiguration revision: %d/%d", "🖥 <b>%s</b> (شمارهٔ %d)\nوضعیت: %s\nعامل نود: %s (%s)\nXray: %s (%s)\nپردازنده: %.1f%% · حافظه: %.1f%%\nنسخهٔ تنظیمات: %d/%d", "🖥 <b>%s</b> (№%d)\nСостояние: %s\nАгент: %s (%s)\nXray: %s (%s)\nCPU: %.1f%% · RAM: %.1f%%\nРевизия конфигурации: %d/%d"),
			escape(node.Name), node.ID, escape(node.Status), escape(node.AgentStatus), escape(node.AgentVersion), escape(node.XrayStatus), escape(node.XrayVersion), node.CPUPercent, node.MemoryPercent, node.AppliedRevision, node.DesiredRevision), nil)
		return
	}
	nodes, err := b.nodes.List(ctx)
	if err != nil {
		b.reply(ctx, settings, chatID, localText(ctx, "Could not load nodes. Please try again.", "نودها بارگذاری نشدند. دوباره تلاش کنید.", "Не удалось загрузить узлы. Попробуйте снова."), nil)
		return
	}
	if len(nodes) == 0 {
		b.reply(ctx, settings, chatID, localText(ctx, "No nodes configured.", "هنوز نودی تنظیم نشده است.", "Узлы ещё не настроены."), nil)
		return
	}
	lines := []string{localText(ctx, "<b>AntiMage nodes</b>", "<b>نودهای AntiMage</b>", "<b>Узлы AntiMage</b>")}
	for i, node := range nodes {
		if i == 20 {
			lines = append(lines, fmt.Sprintf(localText(ctx, "… and %d more nodes", "… و %d نود دیگر", "… и ещё %d узлов"), len(nodes)-i))
			break
		}
		lines = append(lines, fmt.Sprintf("<code>%d</code> %s · %s / %s", node.ID, escape(node.Name), escape(node.Status), escape(node.AgentStatus)))
	}
	lines = append(lines, localText(ctx, "Use /node &lt;id&gt; for live metrics.", "برای دیدن آمار زنده /node &lt;شناسه&gt; را بزنید.", "Для текущих показателей: /node &lt;ID&gt;."))
	b.reply(ctx, settings, chatID, strings.Join(lines, "\n"), nil)
}
