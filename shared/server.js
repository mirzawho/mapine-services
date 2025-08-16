const express = require('express');
const cors = require('cors');
const uuid = require('uuid');
const path = require('path');

const app = express();

app.use(
	cors({
		exposedHeaders: ['.id', '.took'],
	})
);
app.use(express.json({limit: '2mb'}));

app.use('/public', express.static(path.join(__dirname, '..' , 'public')));

app.use((req, res, next) => {
	req.meta = {
		id: uuid.v4(),
		connectedBy: "http",
		ip: req.headers['cf-connecting-ip'] || req.headers["x-forwarded-for"] || req.socket.remoteAddress,
		userAgent: req.headers["user-agent"],
		cache: false,
		requestedAt: Date.now(),
		responsedAt: null,
		response: null,
		flavor: req.headers['x-flavor'],
		version: req.headers['x-version'],
		app: req.headers['x-app']
	};

	if (req.headers["authorization"]) {
		let token = req.headers["authorization"];

		if (token.startsWith('Bearer')) {
			token = token.replace('Bearer', '').trim();
		}

		if(token.startsWith('Basic')) {
			token = token.replace('Basic', '').trim();

			const buff = Buffer.from(token, 'base64');

			token = buff.toString('ascii');

			const [username, password] = token.split(':');

			token = {
				username,
				password
			}
		}

		if (token.length !== 0) {
			req.meta.accessToken = token;
		}
	}

	// x-api-key
	if(req.headers['x-api-key'] || req.query['apiKey']) {
		req.meta.apiKey = req.headers['x-api-key'] || req.query['apiKey'];
	}

	// x-cache
	if(req.headers['x-cache'] || req.query['xCache']) {
		req.meta.cache = true;
	}

	const _resJson = res.json;

	res.json = (body) => {
		req.meta.status = body.status ?? 200;
		req.meta.responsedAt = Date.now();
		req.meta.took = req.meta.responsedAt - req.meta.requestedAt;
		
		res.set('.id', req.meta.id);
		res.set('.took', req.meta.took);
		
		if(body.meta) {
			body.meta['took'] = req.meta.took;
		}
		
		body['ok'] = req.meta.status == 200;

		req.meta.response = body;

		_resJson.call(res, body);

		return req.meta;
	};

	next();
});

module.exports = app;
