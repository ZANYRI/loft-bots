package state

import (
	"encoding/json"
	"time"

	"gorm.io/gorm"

	"loft-bots/internal/db"
)

type FSM struct {
	db *gorm.DB
}

func NewFSM(db_ *gorm.DB) *FSM {
	return &FSM{db: db_}
}

func (f *FSM) GetState(telegramID int64, bot string) (string, map[string]interface{}, error) {
	var us db.UserState
	err := f.db.Where("telegram_id = ? AND bot = ?", telegramID, bot).First(&us).Error
	if err != nil {
		return "", nil, err
	}

	var data map[string]interface{}
	if us.Data != "" {
		json.Unmarshal([]byte(us.Data), &data)
	}

	return us.State, data, nil
}

func (f *FSM) SetState(telegramID int64, bot, state string, data map[string]interface{}) error {
	jsonData := "{}"
	if data != nil {
		b, _ := json.Marshal(data)
		jsonData = string(b)
	}

	us := &db.UserState{
		TelegramID: telegramID,
		Bot:        bot,
		State:      state,
		Data:       jsonData,
		UpdatedAt:  time.Now(),
	}

	return f.db.Save(us).Error
}

func (f *FSM) UpdateData(telegramID int64, bot string, data map[string]interface{}) error {
	currentState, existingData, err := f.GetState(telegramID, bot)
	if err != nil {
		return f.SetState(telegramID, bot, "", data)
	}
	if existingData == nil {
		existingData = make(map[string]interface{})
	}

	for k, v := range data {
		existingData[k] = v
	}

	return f.SetState(telegramID, bot, currentState, existingData)
}

func (f *FSM) ClearState(telegramID int64, bot string) error {
	return f.db.Where("telegram_id = ? AND bot = ?", telegramID, bot).Delete(&db.UserState{}).Error
}

func (f *FSM) IsInState(telegramID int64, bot, state string) bool {
	us, _, err := f.GetState(telegramID, bot)
	if err != nil {
		return false
	}
	return us == state
}
