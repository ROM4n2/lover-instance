package narrative

import (
	"fmt"
	"strings"
)

// RenderPrompt turns the aggregates into the Chinese instruction sent to the
// model.
//
// Because it is a pure function of Input, it can only ever print (a) the fixed
// template below and (b) values read out of Input — and Input has no field that
// can hold a message. Shares are printed to one decimal and means to two, so a
// raw float never leaks into the prompt. The model is asked to describe, not to
// judge.
func RenderPrompt(i Input) string {
	var b strings.Builder

	b.WriteString("你是一名只做描述的报告助手。下面是关于一段聊天记录的聚合统计，请仅根据这些数字写一段中文说明。\n")
	fmt.Fprintf(&b, "时间范围：%s 至 %s，共 %d 天。\n", i.RangeStart, i.RangeEnd, i.Days)
	fmt.Fprintf(&b, "消息总数：%d 条，其中我 %d 条，对方 %d 条。\n", i.Messages, i.SelfMessages, i.PartnerMessages)
	if line := sharesLine(i.EmotionShares); line != "" {
		fmt.Fprintf(&b, "情绪占比：%s。\n", line)
	}
	if line := sharesLine(i.SentimentShares); line != "" {
		fmt.Fprintf(&b, "倾向占比：%s。\n", line)
	}
	fmt.Fprintf(&b, "我的高频词：%s。\n", joinWords(i.TopWordsSelf))
	fmt.Fprintf(&b, "对方的高频词：%s。\n", joinWords(i.TopWordsPartner))
	fmt.Fprintf(&b, "常见表情：%s。\n", joinWords(i.TopExpressions))
	if line := trendLine(i.TrendPoints); line != "" {
		fmt.Fprintf(&b, "每日趋势：%s。\n", line)
	}
	if line := monthlyLine(i.MonthlyTop); line != "" {
		fmt.Fprintf(&b, "月度高频词：%s。\n", line)
	}

	b.WriteString("写作要求：\n")
	b.WriteString("1. 只描述上面的数字，不要编造数字以外的事实。\n")
	b.WriteString("2. 不要下关系判断，不要给结论。\n")
	b.WriteString("3. 不要用第二人称指责对方。\n")
	b.WriteString("4. 遇到样本量太小的项，请直接写“样本不足”。\n")
	b.WriteString("5. 正文控制在 150 到 250 字。\n")

	return b.String()
}

// pct renders a share in [0,1] as a percentage with one fixed decimal, so the
// prompt never carries a raw ratio like 0.123456.
func pct(v float64) string {
	return fmt.Sprintf("%.1f%%", v*100)
}

// sharesLine renders a share map in key order, one decimal each.
func sharesLine(shares map[string]float64) string {
	if len(shares) == 0 {
		return ""
	}
	keys := sortedKeys(shares)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s %s", k, pct(shares[k])))
	}
	return strings.Join(parts, "、")
}

// joinWords renders a token list, or "无" when the list is empty.
func joinWords(words []string) string {
	if len(words) == 0 {
		return "无"
	}
	return strings.Join(words, "、")
}

// trendLine renders each day's mean and spread to two decimals.
func trendLine(points []TrendPoint) string {
	if len(points) == 0 {
		return ""
	}
	parts := make([]string, 0, len(points))
	for _, p := range points {
		parts = append(parts, fmt.Sprintf("%s 均值 %.2f（±%.2f，%d 条）", p.Day, p.Mean, p.StdDev, p.Count))
	}
	return strings.Join(parts, "；")
}

// monthlyLine renders the top words per month, months in key order.
func monthlyLine(months map[string][]string) string {
	if len(months) == 0 {
		return ""
	}
	keys := sortedKeys(months)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s：%s", k, joinWords(months[k])))
	}
	return strings.Join(parts, "；")
}
