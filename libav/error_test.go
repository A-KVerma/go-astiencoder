package astilibav

import (
	"testing"

	"github.com/asticode/goav/avutil"
	"github.com/stretchr/testify/assert"
)

func TestAvError(t *testing.T) {
	err := NewAvError(avutil.AVERROR_EPIPE)
	assert.Equal(t, "Broken pipe", err.Error())
	assert.False(t, err.Is(NewAvError(avutil.AVERROR_EOF)))
	assert.True(t, err.Is(NewAvError(avutil.AVERROR_EPIPE)))
}
