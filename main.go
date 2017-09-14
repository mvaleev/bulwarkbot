package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/dchest/captcha"
	"github.com/spf13/viper"
	//"gopkg.in/telegram-bot-api.v4"
	"github.com/mvaleev/telegram-bot-api"
)

type (
	msgResponse struct {
		chatID int64
		name   string
		file   bool
	}
	msgRequest struct {
		chatID    int64
		messageID int
	}
)

var (
	configFile    string
	chnResp       = make(chan msgResponse, 5)
	chnReq        = make(chan msgRequest, 5)
	store         = captcha.NewMemoryStore(10000, captcha.Expiration)
	chatNameStore = make(map[string]string)
)

func init() {
	captcha.SetCustomStore(store)
}

func main() {
	flag.StringVar(&configFile, "configFile", "config.yml", "configuration file")
	flag.Parse()

	viper.SetConfigType("yaml")
	viper.SetConfigFile(configFile)
	err := viper.ReadInConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "\nError: %s \n\n", err)
		fmt.Fprintf(os.Stderr, "Usage of %s:\n", os.Args[0])
		flag.PrintDefaults()
		os.Exit(1)
	}

	go getResp()

	// regexp for check digits message
	regDigists, err := regexp.Compile(`\A[\d]+$`)
	if err != nil {
		log.Printf("Error on regexp.Compile: %v", err)
	}
	// regexp for check link to chat
	regURL, err := regexp.Compile(`https://t.me/[a-zA-Z0-9]+$`)
	if err != nil {
		log.Printf("Error on regexp.Compile: %v", err)
	}

	botAPIKey := viper.GetString("api_key")
	bot, err := tgbotapi.NewBotAPI(botAPIKey)
	if err != nil {
		log.Panicf("Error on NewBotAPI: %v", err)
	}

	bot.Debug = true
	log.Printf("Authorized on account %s", bot.Self.UserName)

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates, _ := bot.GetUpdatesChan(u)

	for {
		select {
		case update := <-updates:
			userID := update.Message.From.ID
			chatID := update.Message.Chat.ID
			msgID := update.Message.MessageID
			msgText := update.Message.Text

			if !update.Message.Chat.IsGroup() && !update.Message.Chat.IsSuperGroup() {
				switch msgText {
				case "/start":
					text := `Отправь ссылку на группу, где я тебя забанил. Например: https://t.me/telegramchat`
					msg := tgbotapi.NewMessage(chatID, text)
					bot.Send(msg)
				case "code", "Code":
					chnReq <- msgRequest{chatID, msgID}
				default:
					if regDigists.MatchString(msgText) {
						if captcha.VerifyString(strconv.FormatInt(chatID, 10), msgText) {
							chatName := chatNameStore[strconv.Itoa(userID)]

							err := unbanChatMember(bot, chatName, userID)
							if err != nil {
								log.Printf("Error on restrictChatMember: %v", err)
								text := `Ошибка. Попробуйте попозже.`
								msg := tgbotapi.NewMessage(chatID, text)
								bot.Send(msg)
							} else {
								delete(chatNameStore, strconv.Itoa(userID))
								text := `Проверочный код верный. Все ограничения в @` + chatName + ` сняты. Приятного общения.`
								msg := tgbotapi.NewMessage(chatID, text)
								bot.Send(msg)
							}

						} else {
							text := `Не верный проверочный код. Попробуй еще раз или начни со /start`
							msg := tgbotapi.NewMessage(chatID, text)
							bot.Send(msg)
						}
					}
					if regURL.MatchString(msgText) {
						chatName := strings.Replace(msgText, "https://t.me/", "", -1)

						chatNameStore[strconv.Itoa(userID)] = chatName

						text := `Теперь отправь слово code, чтобы получить проверочный код.`
						msg := tgbotapi.NewMessage(chatID, text)
						bot.Send(msg)

					}
					if !regDigists.MatchString(msgText) && !regURL.MatchString(msgText) {
						text := `Не верная команда. начни со /start`
						msg := tgbotapi.NewMessage(chatID, text)
						bot.Send(msg)
					}
				}
			}

			if update.Message.NewChatMembers != nil {
				userIDNew := (*update.Message.NewChatMembers)[0].ID
				userNameNew := (*update.Message.NewChatMembers)[0].UserName
				userFNameNew := (*update.Message.NewChatMembers)[0].FirstName
				userLNameNew := (*update.Message.NewChatMembers)[0].LastName
				chatName := update.Message.Chat.UserName
				chatTitle := update.Message.Chat.Title

				err := restrictChatMember(bot, chatName, userIDNew)
				if err != nil {
					log.Printf("Error on restrictChatMember: %v", err)
				}

				text := `Добро пожаловать в чат ` + chatTitle + "\n" +
					userFNameNew + " " + userLNameNew + " (" + userNameNew + ").\n" +
					"В данный момент ты не можешь писать в эту группу. Чтобы это исправить, напиши в мне в личку (@" + bot.Self.UserName + ").\n"
				msg := tgbotapi.NewMessage(chatID, text)
				bot.Send(msg)
			}

		case resp := <-chnResp:
			if resp.file {
				msg := tgbotapi.NewPhotoUpload(resp.chatID, nil)
				msg.File = resp.name
				msg.Caption = "Код действителен в течении 10 минут."
				bot.Send(msg)

				err := os.Remove(resp.name)
				if err != nil {
					log.Printf("Error delete file %v: %v", resp.name, err)
				}
			}
		}
	}
}

func getResp() {
	// goroutine for magic
	for msg := range chnReq {
		var name string
		var err error
		file := false
		d := captcha.RandomDigits(10)

		if name, err = newCapchaImg(strconv.FormatInt(msg.chatID, 10), d); err == nil {
			store.Set(strconv.FormatInt(msg.chatID, 10), d)
			file = true
		} else {
			log.Printf("Error on newCapchaImg: %v", err)
		}

		chnResp <- msgResponse{msg.chatID, name, file}
	}

}

func newCapchaImg(id string, digits []byte) (string, error) {
	var w io.WriterTo
	name := "/tmp/" + id + ".png"

	f, err := os.Create(name)
	if err != nil {
		return "", err
	}
	defer f.Close()

	w = captcha.NewImage(id, digits, captcha.StdWidth, captcha.StdHeight)
	_, err = w.WriteTo(f)
	if err != nil {
		return "", err
	}
	return name, nil
}

func checkString(str string) bool {
	reg, err := regexp.Compile(`\A[\d]{10,10}$`)
	if err != nil {
		log.Printf("Error on regexp.Compile: %v", err)
	}

	if reg.MatchString(str) {
		return true
	}
	return false
}

func restrictChatMember(b *tgbotapi.BotAPI, chatName string, userID int) error {
	config := tgbotapi.RestrictChatMemberConfig{}
	config.SuperGroupUsername = "@" + chatName
	config.UserID = userID
	config.CanAddWebPagePreviews = false
	config.CanSendMediaMessages = false
	config.CanSendMessages = false
	config.CanSendOtherMessages = false

	msg, err := b.RestrictChatMember(config)
	log.Printf("msg from api on restrictChatMember: %v", msg)
	return err
}

func unbanChatMember(b *tgbotapi.BotAPI, chatName string, userID int) error {
	config := tgbotapi.ChatMemberConfig{}
	config.SuperGroupUsername = "@" + chatName
	config.UserID = userID

	msg, err := b.UnbanChatMember(config)
	log.Printf("msg from api on unbanChatMember: %v", msg)
	return err
}
