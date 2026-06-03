package internal

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestValidateReplicaMasterURL(t *testing.T) {
	oldAllowInsecure := os.Getenv("ALLOW_INSECURE_SYNC")
	oldMasterIP := os.Getenv("REPLICA_MASTER_IP")
	oldMasterPort := os.Getenv("MASTER_API_PORT")

	t.Cleanup(func() {
		_ = os.Setenv("ALLOW_INSECURE_SYNC", oldAllowInsecure)
		_ = os.Setenv("REPLICA_MASTER_IP", oldMasterIP)
		_ = os.Setenv("MASTER_API_PORT", oldMasterPort)
	})

	tests := []struct {
		name          string
		masterURL     string
		allowInsecure string
		replicaIP     string
		masterPort    string
		expectedURL   string
		expectError   bool
	}{
		{
			name:          "valid https master url",
			masterURL:     "https://master.example.local",
			allowInsecure: "false",
			expectedURL:   "https://master.example.local",
			expectError:   false,
		},
		{
			name:          "http master url allowed in insecure mode",
			masterURL:     "http://10.10.10.3:8080",
			allowInsecure: "true",
			expectedURL:   "http://10.10.10.3:8080",
			expectError:   false,
		},
		{
			name:          "http master url denied in secure mode",
			masterURL:     "http://10.10.10.3:8080",
			allowInsecure: "false",
			expectError:   true,
		},
		{
			name:          "fallback to replica master ip in insecure mode",
			masterURL:     "",
			allowInsecure: "true",
			replicaIP:     "10.10.10.3",
			masterPort:    "8080",
			expectedURL:   "http://10.10.10.3:8080",
			expectError:   false,
		},
		{
			name:          "fallback uses default port",
			masterURL:     "",
			allowInsecure: "true",
			replicaIP:     "10.10.10.3",
			masterPort:    "",
			expectedURL:   "http://10.10.10.3:8080",
			expectError:   false,
		},
		{
			name:          "fallback denied when insecure mode disabled",
			masterURL:     "",
			allowInsecure: "false",
			replicaIP:     "10.10.10.3",
			expectError:   true,
		},
		{
			name:          "fallback invalid replica master ip",
			masterURL:     "",
			allowInsecure: "true",
			replicaIP:     "not-an-ip",
			masterPort:    "8080",
			expectError:   true,
		},
		{
			name:          "fallback invalid master port",
			masterURL:     "",
			allowInsecure: "true",
			replicaIP:     "10.10.10.3",
			masterPort:    "99999",
			expectError:   true,
		},
		{
			name:          "invalid url without scheme",
			masterURL:     "master.example.local:8080",
			allowInsecure: "true",
			expectError:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_ = os.Setenv("ALLOW_INSECURE_SYNC", tt.allowInsecure)
			_ = os.Setenv("REPLICA_MASTER_IP", tt.replicaIP)
			_ = os.Setenv("MASTER_API_PORT", tt.masterPort)

			result, err := ValidateMasterURL(tt.masterURL)
			if tt.expectError {
				assert.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.expectedURL, result)
		})
	}
}
