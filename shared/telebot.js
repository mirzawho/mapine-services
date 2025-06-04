module.exports = {
    async send(message) {
        const telegramApiUrl = `https://api.telegram.org/bot${process.env.TELEGRAM_BOT_TOKEN}/sendMessage`;

        try {
            await fetch(telegramApiUrl, {
                method: 'POST',
                headers: {
                    'Content-Type': 'application/json',
                },
                body: JSON.stringify({
                    chat_id: process.env.TELEGRAM_CHAT_ID,
                    text: message,
                }),
            });
        } catch (error) {
            console.error('Failed to send Telegram message:', error);
        }
    },
}