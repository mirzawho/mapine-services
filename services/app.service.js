"use strict";

const handshake = require('../app/handshake.json');
const { helper } = require('../shared');

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
				const userVersion = helper.number(ctx.meta.version ?? '0.0.0');
				const latestVersion = helper.number(handshake.version_latest ?? '0.0.0');
				const deprecatedVersion = helper.number(handshake.version_deprecated ?? '0.0.0');

				return {
					status: 200,
					data: {
						...handshake,
						isDeprecated: userVersion < deprecatedVersion,
						isLatest: userVersion >= latestVersion,
					},
				}
			}
		}
	},
}