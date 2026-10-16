package wa

import (
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

func (c *Client) RegularLowPolicyStatus() whatsmeow.RegularLowPolicyStatus {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil {
		return whatsmeow.RegularLowPolicyStatus{}
	}
	return cli.RegularLowPolicyStatus()
}

func (c *Client) RegularLowChatProjection(jid types.JID) whatsmeow.RegularLowChatProjection {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil {
		return whatsmeow.RegularLowChatProjection{}
	}
	return cli.RegularLowChatProjection(jid)
}

func (c *Client) InvalidateRegularLowPolicy() {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli != nil {
		cli.InvalidateRegularLowPolicy()
	}
}
