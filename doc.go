// Package e2b is an unofficial Go SDK for E2B sandboxes (https://e2b.dev).
//
// A Client authenticates against the E2B control plane and creates Sandboxes.
// Each Sandbox exposes file and process operations backed by envd (the agent
// that runs inside every E2B microVM), over ConnectRPC.
//
// # Example
//
//	c := e2b.NewClient("E2B_API_KEY")
//	sb, err := c.CreateSandbox(ctx, e2b.CreateRequest{
//	    TemplateID: "base",
//	    Timeout:    5 * time.Minute,
//	})
//	if err != nil {
//	    return err
//	}
//	defer sb.Destroy(ctx)
//
//	_, err = sb.WriteFile(ctx, "/workspace/hello.txt", []byte("hi"))
//	if err != nil {
//	    return err
//	}
//
//	result, err := sb.Exec(ctx, e2b.ExecRequest{
//	    Command: []string{"cat", "/workspace/hello.txt"},
//	})
//	// result.Stdout == "hi"
//
// This SDK is not affiliated with or endorsed by E2B. See
// https://github.com/e2b-dev/E2B/issues/985 for upstream status.
package e2b
