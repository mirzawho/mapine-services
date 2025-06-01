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
				try {
					
					const userVersion = helper.number.formatVersion(ctx.meta.version ?? '0.0.0');
					const latestVersion = helper.number.formatVersion(handshake.version_latest ?? '0.0.0');
					const deprecatedVersion = helper.number.formatVersion(handshake.version_deprecated ?? '0.0.0');
	
					return {
						status: 200,
						data: {
							...handshake,
							isDeprecated: userVersion < deprecatedVersion,
							isUpdateAvailable: userVersion < latestVersion,
						},
					}
				} catch (error) {
					console.error(error);

					return {
						status: 500
					}
				}
			}
		}
	},
}