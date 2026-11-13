// Command aopspike is a throwaway probe: it opens one AOP stream against a
// running `aiscan serve`, opens a session, runs one turn and prints events.
// It exists to prove the wire before the executor is rewritten on top of it.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/kksty/tianwu/internal/aop"
)

func main() {
	url := flag.String("url", "http://127.0.0.1:18766", "harness server base URL")
	token := flag.String("token", "testkey", "access key")
	node := flag.String("node", "", "node id to pin the session to (empty = let the server pick)")
	prompt := flag.String("prompt", "say hi", "prompt for one turn")
	timeout := flag.Duration("timeout", 120*time.Second, "overall deadline")
	// The wire is what the executor has to translate, so being able to print it
	// verbatim is the point of keeping this probe around.
	asJSON := flag.Bool("json", false, "print each event as protojson (the same shape stream-json uses)")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	c := aop.New(*url, *token)
	conn, err := c.Dial(ctx)
	if err != nil {
		log.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	fmt.Println("dialed", *url)

	sess, err := conn.OpenSession(ctx, *node, "tianwu spike", "")
	if err != nil {
		log.Fatalf("open session: %v", err)
	}
	fmt.Printf("session=%s state=%s node=%s\n", sess.GetId(), sess.GetState(), sess.GetNodeId())

	if err := conn.Watch(ctx, sess.GetId(), ""); err != nil {
		log.Fatalf("watch: %v", err)
	}
	fmt.Println("watching")

	turnID := fmt.Sprintf("turn-%d", time.Now().UnixNano())
	receipt, err := conn.RunTurn(ctx, sess.GetId(), turnID, *prompt)
	if err != nil {
		log.Fatalf("run turn: %v", err)
	}
	fmt.Printf("turn=%s state=%s\n", receipt.GetTurnId(), receipt.GetState())

	for {
		select {
		case <-ctx.Done():
			fmt.Println("timeout")
			return
		case ev, ok := <-conn.Events():
			if !ok {
				fmt.Println("stream closed")
				return
			}
			kind := "?"
			if p := ev.GetPayload(); p != nil {
				kind = fmt.Sprintf("%T", p)
			}
			if *asJSON {
				raw, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(ev)
				if err != nil {
					fmt.Printf("event seq=%d %s (marshal: %v)\n", ev.GetSeq(), kind, err)
				} else {
					fmt.Printf("%s\n", raw)
				}
			} else {
				fmt.Printf("event seq=%d %s\n", ev.GetSeq(), kind)
			}
			if ev.GetTurnEnded() != nil || ev.GetError() != nil {
				_ = conn.CloseSession(context.Background(), sess.GetId(), "done")
				return
			}
		}
	}
}
