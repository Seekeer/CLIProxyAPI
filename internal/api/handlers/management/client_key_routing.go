package management

import (
	"bytes"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"gopkg.in/yaml.v3"
	"net/http"
	"os"
)

// PrepareUpstreamRoutingKeys initializes identities without exposing provider secrets.
func (h *Handler) PrepareUpstreamRoutingKeys(c *gin.Context) {
	h.mu.Lock()
	defer h.mu.Unlock()
	raw, err := os.ReadFile(h.configFilePath)
	if err != nil {
		c.JSON(500, gin.H{"error": "read_failed"})
		return
	}
	raw, _, err = config.NormalizeConfigLayout(raw, true)
	if err != nil {
		c.JSON(500, gin.H{"error": "invalid_config"})
		return
	}
	var doc yaml.Node
	if err = yaml.Unmarshal(raw, &doc); err != nil || len(doc.Content) == 0 {
		c.JSON(500, gin.H{"error": "invalid_config"})
		return
	}
	root := doc.Content[0]
	baseline, _ := yaml.Marshal(&doc)
	if err = config.EnsureUpstreamRoutingIDs(root, nil); err != nil {
		c.JSON(422, gin.H{"error": "invalid_routing_ids"})
		return
	}
	data, err := yaml.Marshal(&doc)
	if err != nil {
		c.JSON(500, gin.H{"error": "invalid_config"})
		return
	}
	next, err := config.ParseConfigBytes(data)
	if err != nil {
		c.JSON(422, gin.H{"error": "invalid_config"})
		return
	}
	if bytes.Equal(baseline, data) {
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusOK, gin.H{"keys": config.UpstreamRoutingCatalog(root)})
		return
	}
	if err = WriteConfig(h.configFilePath, data); err != nil {
		c.JSON(500, gin.H{"error": "write_failed"})
		return
	}
	next.Home = h.cfg.Home
	h.cfg = next
	h.reloadConfigAfterManagementSaveAsync(c.Request.Context(), h.reloadSnapshotConfigLocked())
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"keys": config.UpstreamRoutingCatalog(root)})
}

// Prevent old full-document editors from silently dropping existing restrictions.
func preserveClientRoutingRestrictions(before *yaml.Node, next *config.Config) bool {
	var previous []config.ClientKeyRouting
	if node := configV8Node(before, []string{"access", "key-routing"}); node != nil {
		if node.Decode(&previous) != nil {
			return false
		}
	}
	for _, old := range previous {
		if old.Mode == "all" {
			continue
		}
		keyStillExists := false
		for _, key := range next.APIKeys {
			if key == old.APIKey {
				keyStillExists = true
			}
		}
		if !keyStillExists {
			continue
		}
		found := false
		for _, policy := range next.ClientKeyRouting {
			if policy.APIKey == old.APIKey {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func validClientRoutingReferences(root, before *yaml.Node, next *config.Config) bool {
	known := make(map[string]bool)
	for _, key := range config.UpstreamRoutingCatalog(root) {
		known[key.ID] = true
	}
	var previous []config.ClientKeyRouting
	if node := configV8Node(before, []string{"access", "key-routing"}); node != nil {
		if node.Decode(&previous) != nil {
			return false
		}
	}
	for _, policy := range next.ClientKeyRouting {
		for _, id := range policy.UpstreamIDs {
			if known[id] {
				continue
			}
			retained := false
			for _, old := range previous {
				if old.ID == policy.ID {
					for _, oldID := range old.UpstreamIDs {
						if oldID == id {
							retained = true
						}
					}
				}
			}
			if !retained {
				return false
			}
		}
	}
	return true
}
