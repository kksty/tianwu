package aop

import (
	"context"
	"fmt"
	"strings"

	"github.com/kksty/tianwu/internal/aop/command"
	pb "github.com/kksty/tianwu/internal/aop/pb"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

// Command uses the harness's direct-command channel, not a model turn. Watch
// must already be active. A receipt only acknowledges dispatch; completion is
// the native message with its CommandDetail extension. The session is dedicated to
// one command at a time so an earlier command cannot be mistaken for this one.
func (c *Conn) Command(ctx context.Context, sessionID, line string) (string, error) {
	message := &command.CommandProtocolMessage{Message: &command.CommandProtocolMessage_Request{Request: &command.CommandRequest{SessionId: sessionID, Line: line}}}
	payload, err := anypb.New(message)
	if err != nil {
		return "", err
	}
	id := newID()
	raw, err := proto.Marshal(&pb.Envelope{Id: id, Payload: payload})
	if err != nil {
		return "", err
	}
	if err := c.sock.WriteBinary(raw); err != nil {
		return "", err
	}
	for {
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("命令结果未确认，不自动重发：%w", ctx.Err())
		case err := <-c.errs:
			return "", err
		case env, ok := <-c.replies:
			if !ok {
				return "", fmt.Errorf("宿主断开")
			}
			if env.ReplyTo != id {
				continue
			}
			var core pb.ProtocolMessage
			if env.Payload.UnmarshalTo(&core) == nil && core.GetProtocolError() != nil {
				return "", fmt.Errorf("宿主拒绝命令：%s", core.GetProtocolError().GetMessage())
			}
			var response command.CommandProtocolMessage
			if env.Payload.UnmarshalTo(&response) != nil || response.GetReceipt() == nil {
				return "", fmt.Errorf("命令回执无法解析")
			}
		case ev, ok := <-c.events:
			if !ok {
				return "", fmt.Errorf("命令事件流结束")
			}
			if ev.SessionId != sessionID {
				continue
			}
			if failure := ev.GetError(); failure != nil {
				return "", fmt.Errorf("命令失败：%s", failure.Message)
			}
			matched := false
			for _, ext := range ev.Extensions {
				if ext.MessageName() != "cyber.agent.CommandDetail" {
					continue
				}
				commandLine, err := commandDetailLine(ext.Value)
				if err != nil {
					return "", err
				}
				matched = commandLine == line
			}
			if !matched || ev.GetMessage() == nil {
				continue
			}
			var text strings.Builder
			for _, part := range ev.GetMessage().Content {
				if part.GetText() != nil {
					text.WriteString(part.GetText().Text)
				}
			}
			return text.String(), nil
		}
	}
}

// CommandDetail is a harness extension, not a second command protocol. Decode
// only its declared line field without importing unrelated provider schemas.
func commandDetailLine(raw []byte) (string, error) {
	for len(raw) > 0 {
		n, t, nbytes := protowire.ConsumeTag(raw)
		if nbytes < 0 {
			return "", protowire.ParseError(nbytes)
		}
		raw = raw[nbytes:]
		if n == 1 && t == protowire.BytesType {
			value, nbytes := protowire.ConsumeString(raw)
			if nbytes < 0 {
				return "", protowire.ParseError(nbytes)
			}
			return value, nil
		}
		nbytes = protowire.ConsumeFieldValue(n, t, raw)
		if nbytes < 0 {
			return "", protowire.ParseError(nbytes)
		}
		raw = raw[nbytes:]
	}
	return "", fmt.Errorf("命令详情缺少 line")
}
