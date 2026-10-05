package fixture

// The isolation module's dependency set is frozen before delegation. These
// blank imports keep every planned dependency in go.mod so delegated work
// never edits go.mod or go.sum.
import (
	_ "github.com/aws/aws-sdk-go-v2/credentials"
	_ "github.com/aws/aws-sdk-go-v2/service/s3"
	_ "github.com/modelcontextprotocol/go-sdk/mcp"
)
