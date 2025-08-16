const axios = require('axios');

module.exports = {
    async send(message) {
        try {
            const options = {
                method: 'POST',
                url: 'https://telebot.sayheymirza.workers.dev/api/v1/send-message',
                headers: { 'Content-Type': 'application/json' },
                data: {
                    'chat_id': process.env.TELEGRAM_CHAT_ID,
                    'token': process.env.TELEGRAM_BOT_TOKEN,
                    'message': message,
                }
            };

            return await axios.request(options);
        } catch (error) {
            console.error('Failed to send Telegram message');
        }
    },
}