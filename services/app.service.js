"use strict";

const handshake = require('../app/handshake.json');

/**
 * @typedef {import('moleculer').ServiceSchema} ServiceSchema Moleculer's Service Schema
 * @typedef {import('moleculer').Context} Context Moleculer's Context
 */

/** @type {ServiceSchema} */
module.exports = {
	name: "app",

	/**
	 * Actions
	 */
	actions: {
		handshake: {
			visibility: 'published',
			handler(ctx) {
				return {
					status: 200,
					data: handshake,
				}
			}
		}
	},
}