package liercd

import "context"

// NewRoutedForTest is NewRouted with base URLs, so other packages' tests can
// route to httptest servers. Production code uses NewRouted.
func NewRoutedForTest(mainBase, nextBase string, instanceOf func(ctx context.Context, id string) (string, error)) *Client {
	return newRoutedBase(mainBase, nextBase, instanceOf)
}
