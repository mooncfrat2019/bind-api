package internal

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestConfigTransform(t *testing.T) {
	transform := ConfigTransform{
		MasterIP:   "192.168.1.100",
		ZoneType:   "slave",
		ZoneSubdir: "slaves",
	}

	originalBody := `zone "example.com" IN {
        type master;
        file "example.com.zone";
        allow-update { none; };
    }`

	rs := &ReplicaSync{Transform: transform}
	transformed := rs.transformZoneBlocks(originalBody)

	assert.Contains(t, transformed, "type slave")
}

func TestTransformOptionsBlock(t *testing.T) {
	rs := &ReplicaSync{}

	content := `options {
        listen-on-v6 port 53 { any; };
        also-notify { 192.168.1.1; };
    }`

	transformed := rs.transformOptionsBlock(content)
	assert.NotContains(t, transformed, "also-notify")
}
