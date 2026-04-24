package geo

import (
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestStaticResolver_FirstMatchWins(t *testing.T) {
	r, err := NewStaticResolver("203.0.113.0/24=US;203.0.113.0/25=CA")
	require.NoError(t, err)
	cc, err := r.LookupCountry(net.ParseIP("203.0.113.5"))
	require.NoError(t, err)
	require.Equal(t, "US", cc)
}

func TestStaticResolver_UnknownOnMiss(t *testing.T) {
	r, _ := NewStaticResolver("203.0.113.0/24=US")
	_, err := r.LookupCountry(net.ParseIP("10.0.0.1"))
	require.ErrorIs(t, err, ErrUnknown)
}

func TestStaticResolver_BadCC(t *testing.T) {
	_, err := NewStaticResolver("203.0.113.0/24=USA")
	require.Error(t, err)
}

// stubResolver counts calls so we can verify caching.
type stubResolver struct{ calls atomic.Int64 }

func (s *stubResolver) LookupCountry(ip net.IP) (string, error) {
	s.calls.Add(1)
	if ip.Equal(net.ParseIP("1.2.3.4")) {
		return "US", nil
	}
	return "", ErrUnknown
}

func TestCachingResolver_HitsInnerOncePerIP(t *testing.T) {
	s := &stubResolver{}
	c := NewCachingResolver(s, time.Minute)
	for i := 0; i < 5; i++ {
		_, _ = c.LookupCountry(net.ParseIP("1.2.3.4"))
		_, _ = c.LookupCountry(net.ParseIP("8.8.8.8"))
	}
	require.EqualValues(t, 2, s.calls.Load())
}

func TestCachingResolver_CachesUnknown(t *testing.T) {
	s := &stubResolver{}
	c := NewCachingResolver(s, time.Minute)
	_, err := c.LookupCountry(net.ParseIP("8.8.8.8"))
	require.True(t, errors.Is(err, ErrUnknown))
	_, err2 := c.LookupCountry(net.ParseIP("8.8.8.8"))
	require.True(t, errors.Is(err2, ErrUnknown))
	require.EqualValues(t, 1, s.calls.Load())
}
