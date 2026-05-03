package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolveSource(t *testing.T) {
	require.Equal(t, "manual", resolveSource(""))
	require.Equal(t, "manual", resolveSource("anything-else"))
	require.Equal(t, "manual", resolveSource("manual"))
	require.Equal(t, "bulk", resolveSource("bulk"))
	require.Equal(t, "api", resolveSource("api"))
}

func TestPtrFloat32OrZero(t *testing.T) {
	v := float32(0.42)
	require.Equal(t, 0.0, ptrFloat32OrZero(nil))
	require.InDelta(t, 0.42, ptrFloat32OrZero(&v), 0.001)
}
