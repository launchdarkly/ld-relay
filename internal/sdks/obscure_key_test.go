package sdks

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestObscureKey(t *testing.T) {
	assert.Equal(t, "********-**-*89abc", ObscureKey("def01234-56-789abc"))
	assert.Equal(t, "sdk-********-**-*89abc", ObscureKey("sdk-def01234-56-789abc"))
	assert.Equal(t, "mob-********-**-*89abc", ObscureKey("mob-def01234-56-789abc"))
	assert.Equal(t, "89abc", ObscureKey("89abc"))
	assert.Equal(t, "9abc", ObscureKey("9abc"))
	assert.Equal(t, "sdk-9abc", ObscureKey("sdk-9abc"))
}
