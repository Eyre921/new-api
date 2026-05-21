package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
)

// withMemoryCacheChannels wires the package-level cache state so
// GetRandomSatisfiedChannelWithFilter exercises the in-memory path. It
// returns a cleanup function the caller MUST defer to restore prior state.
func withMemoryCacheChannels(t *testing.T, channels []*Channel) func() {
	t.Helper()

	prevMemoryCache := common.MemoryCacheEnabled
	channelSyncLock.Lock()
	prevByID := channelsIDM
	prevByGroupModel := group2model2channels
	channelSyncLock.Unlock()

	common.MemoryCacheEnabled = true

	idm := make(map[int]*Channel, len(channels))
	groupModel := map[string]map[string][]int{
		"test-group": {
			"gpt-x": {},
		},
	}
	for _, ch := range channels {
		idm[ch.Id] = ch
		groupModel["test-group"]["gpt-x"] = append(groupModel["test-group"]["gpt-x"], ch.Id)
	}

	channelSyncLock.Lock()
	channelsIDM = idm
	group2model2channels = groupModel
	channelSyncLock.Unlock()

	return func() {
		channelSyncLock.Lock()
		channelsIDM = prevByID
		group2model2channels = prevByGroupModel
		channelSyncLock.Unlock()
		common.MemoryCacheEnabled = prevMemoryCache
	}
}

func boolPtr(b bool) *bool { return &b }

func TestGetRandomSatisfiedChannelWithFilter_DropsDisabledCapability(t *testing.T) {
	priority := int64(10)
	weight := uint(1)

	// Three channels at the same priority; only id=2 has the new capability
	// flag explicitly set to false and should be filtered out.
	chSupported := &Channel{Id: 1, Priority: &priority, Weight: &weight, Status: common.ChannelStatusEnabled}
	chDisabled := &Channel{Id: 2, Priority: &priority, Weight: &weight, Status: common.ChannelStatusEnabled}
	chDefault := &Channel{Id: 3, Priority: &priority, Weight: &weight, Status: common.ChannelStatusEnabled}

	supportedOther := `{"responses_image_generation":true}`
	disabledOther := `{"responses_image_generation":false}`
	chSupported.OtherSettings = supportedOther
	chDisabled.OtherSettings = disabledOther
	// chDefault deliberately leaves OtherSettings empty → SupportsResponsesImageGeneration returns true.

	cleanup := withMemoryCacheChannels(t, []*Channel{chSupported, chDisabled, chDefault})
	defer cleanup()

	filter := func(c *Channel) bool {
		return c.GetOtherSettings().SupportsResponsesImageGeneration()
	}

	// Run a bunch of selections; the disabled channel must never be returned.
	for i := 0; i < 200; i++ {
		channel, err := GetRandomSatisfiedChannelWithFilter("test-group", "gpt-x", 0, filter)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if channel == nil {
			t.Fatalf("expected a channel, got nil")
		}
		if channel.Id == chDisabled.Id {
			t.Fatalf("filter must drop channel %d (responses_image_generation=false)", chDisabled.Id)
		}
	}
}

func TestGetRandomSatisfiedChannelWithFilter_AllDroppedReturnsNil(t *testing.T) {
	priority := int64(10)
	weight := uint(1)

	chDisabled := &Channel{Id: 11, Priority: &priority, Weight: &weight, Status: common.ChannelStatusEnabled}
	chDisabled2 := &Channel{Id: 12, Priority: &priority, Weight: &weight, Status: common.ChannelStatusEnabled}
	chDisabled.OtherSettings = `{"responses_image_generation":false}`
	chDisabled2.OtherSettings = `{"responses_image_generation":false}`

	cleanup := withMemoryCacheChannels(t, []*Channel{chDisabled, chDisabled2})
	defer cleanup()

	channel, err := GetRandomSatisfiedChannelWithFilter("test-group", "gpt-x", 0, func(c *Channel) bool {
		return c.GetOtherSettings().SupportsResponsesImageGeneration()
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if channel != nil {
		t.Fatalf("expected nil channel when all candidates are filtered out, got id=%d", channel.Id)
	}
}

func TestGetRandomSatisfiedChannel_NilFilterUnchanged(t *testing.T) {
	priority := int64(10)
	weight := uint(1)
	ch := &Channel{Id: 21, Priority: &priority, Weight: &weight, Status: common.ChannelStatusEnabled}

	cleanup := withMemoryCacheChannels(t, []*Channel{ch})
	defer cleanup()

	channel, err := GetRandomSatisfiedChannel("test-group", "gpt-x", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if channel == nil || channel.Id != 21 {
		t.Fatalf("expected channel 21, got %v", channel)
	}
}

func TestSupportsResponsesImageGenerationDefaults(t *testing.T) {
	var ch Channel
	// Unset OtherSettings → default supports.
	if !ch.GetOtherSettings().SupportsResponsesImageGeneration() {
		t.Fatalf("default (unset) channel must report as supporting Responses image_generation")
	}

	ch.OtherSettings = `{"responses_image_generation":true}`
	if !ch.GetOtherSettings().SupportsResponsesImageGeneration() {
		t.Fatalf("channel with responses_image_generation=true must report supports")
	}

	ch.OtherSettings = `{"responses_image_generation":false}`
	if ch.GetOtherSettings().SupportsResponsesImageGeneration() {
		t.Fatalf("channel with responses_image_generation=false must report NOT supports")
	}

	settings := ch.GetOtherSettings()
	settings.ResponsesImageGeneration = boolPtr(false)
	if settings.SupportsResponsesImageGeneration() {
		t.Fatalf("local OtherSettings with explicit false pointer must report NOT supports")
	}
}
