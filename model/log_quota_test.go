package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHydrateCurrentTokenQuotaUsesCurrentTokenQuota(t *testing.T) {
	setupUserUpdateTestState(t)

	commonUser := User{
		Id:       11,
		Username: "quota-common-user",
		Password: "password",
		Role:     common.RoleCommonUser,
		Status:   common.UserStatusEnabled,
		Quota:    900,
		AffCode:  "quota-common-aff",
	}
	adminUser := User{
		Id:       12,
		Username: "quota-admin-user",
		Password: "password",
		Role:     common.RoleAdminUser,
		Status:   common.UserStatusEnabled,
		Quota:    800,
		AffCode:  "quota-admin-aff",
	}
	require.NoError(t, DB.Create(&commonUser).Error)
	require.NoError(t, DB.Create(&adminUser).Error)

	commonToken := Token{Id: 21, UserId: commonUser.Id, Key: "quota-common-token", RemainQuota: 300, UsedQuota: 70}
	adminToken := Token{Id: 22, UserId: adminUser.Id, Key: "quota-admin-token", RemainQuota: 200, UsedQuota: 80, UnlimitedQuota: true}
	deletedToken := Token{Id: 23, UserId: commonUser.Id, Key: "quota-deleted-token", RemainQuota: 100, UsedQuota: 20}
	require.NoError(t, DB.Create(&commonToken).Error)
	require.NoError(t, DB.Create(&adminToken).Error)
	require.NoError(t, DB.Create(&deletedToken).Error)
	require.NoError(t, DB.Delete(&deletedToken).Error)

	logs := []*Log{
		{TokenId: commonToken.Id},
		{TokenId: adminToken.Id},
		{TokenId: deletedToken.Id},
		{TokenId: 999},
	}
	require.NoError(t, hydrateCurrentTokenQuota(logs))

	require.NotNil(t, logs[0].TokenRemainQuota)
	assert.Equal(t, 300, *logs[0].TokenRemainQuota)
	require.NotNil(t, logs[0].TokenUsedQuota)
	assert.Equal(t, 70, *logs[0].TokenUsedQuota)
	require.NotNil(t, logs[0].DisplayRemainQuota)
	assert.Equal(t, commonToken.RemainQuota, *logs[0].DisplayRemainQuota)
	require.NotNil(t, logs[0].DisplayUnlimited)
	assert.False(t, *logs[0].DisplayUnlimited)

	require.NotNil(t, logs[1].DisplayRemainQuota)
	assert.Equal(t, adminToken.RemainQuota, *logs[1].DisplayRemainQuota)
	require.NotNil(t, logs[1].DisplayUnlimited)
	assert.True(t, *logs[1].DisplayUnlimited)

	assert.Nil(t, logs[2].DisplayRemainQuota)
	assert.Nil(t, logs[2].DisplayUnlimited)
	assert.Nil(t, logs[3].DisplayRemainQuota)
	assert.Nil(t, logs[3].DisplayUnlimited)

	oldBatchUpdateEnabled := common.BatchUpdateEnabled
	common.BatchUpdateEnabled = true
	addNewRecord(BatchUpdateTypeTokenQuota, commonToken.Id, -25)
	t.Cleanup(func() {
		batchUpdateLocks[BatchUpdateTypeTokenQuota].Lock()
		delete(batchUpdateStores[BatchUpdateTypeTokenQuota], commonToken.Id)
		batchUpdateLocks[BatchUpdateTypeTokenQuota].Unlock()
		common.BatchUpdateEnabled = oldBatchUpdateEnabled
	})

	pendingLogs := []*Log{{TokenId: commonToken.Id}}
	require.NoError(t, hydrateCurrentTokenQuota(pendingLogs))
	require.NotNil(t, pendingLogs[0].DisplayRemainQuota)
	assert.Equal(t, commonToken.RemainQuota-25, *pendingLogs[0].DisplayRemainQuota)
	require.NotNil(t, pendingLogs[0].TokenUsedQuota)
	assert.Equal(t, commonToken.UsedQuota+25, *pendingLogs[0].TokenUsedQuota)
}
