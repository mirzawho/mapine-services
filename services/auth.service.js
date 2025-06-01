"use strict";

const fs = require('fs');
const path = require('path');

/**
 * @typedef {import('moleculer').ServiceSchema} ServiceSchema Moleculer's Service Schema
 * @typedef {import('moleculer').Context} Context Moleculer's Context
 */

/** @type {ServiceSchema} */
module.exports = {
	name: "auth",
	version: 'api.v1',

	/**
	 * Settings
	 */
	settings: {
		filename: path.join(__dirname, process.env['USERS_FILE']),
	},

	/**
	 * Dependencies
	 */
	dependencies: [],

	/**
	 * Actions
	 */
	actions: {
		authenticate: {
			visibility: 'public',
			params: {
				username: {
					type: 'string',
					min: 4,
					max: 12
				},
				password: {
					type: 'string',
					min: 8,
					max: 24
				}
			},
			cache: {
				enabled: true,
				keys: ['username', 'password'],
				ttl: 10,
			},
			handler(ctx) {
				const {username, password} = ctx.params;
				const users = this.load();

				const index = users.findIndex(user => user.username === username && user.password === password);

				if (index !== -1) {
					return {
						id: index,
						...users[index]
					}
				} else {
					return null
				}
			}
		}
	},

	/**
	 * Events
	 */
	events: {},

	/**
	 * Methods
	 */
	methods: {
		load() {
			try {
				return JSON.parse(fs.readFileSync(this.settings.filename, 'utf8'));
			} catch (error) {
				return [];
			}
		},
		save(users = []) {
			fs.writeFileSync(this.settings.filename, JSON.stringify(users, null, 4));
		}
	},

	/**
	 * Service created lifecycle event handler
	 */
	created() {
		// check if file not exists to create it
		if (!fs.existsSync(this.settings.filename)) {
			this.save();
		}
	},

	/**
	 * Service started lifecycle event handler
	 */
	async started() {

	},

	/**
	 * Service stopped lifecycle event handler
	 */
	async stopped() {

	}
};
