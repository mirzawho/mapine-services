"use strict";
const httpMixin = require('./http.mixin');

/**
 * @typedef {import('moleculer').ServiceSchema} ServiceSchema Moleculer's Service Schema
 * @typedef {import('moleculer').Context} Context Moleculer's Context
 */

/** @type {ServiceSchema} */
module.exports = {
	name: "http",

	mixins: [httpMixin],

	/**
	 * Settings
	 */
	settings: {
	},

	/**
	 * Dependencies
	 */
	dependencies: [],

	/**
	 * Actions
	 */
	actions: {
		info: {
			tracing: false,
			visibility: 'published',
			handler() {
				return {
					status: 200,
					info: {
						namespace: this.broker.namespace,
						node: this.broker.nodeID,
					}
				}
			}
		},
		actions: {
			tracing: false,
			visibility: 'published',
			handler() {
				return {
					status: 200,
					actions: this.listActions().map(this.formatAction),
				}
			}
		},
	},

	/**
	 * Events
	 */
	events: {

	},

	/**
	 * Methods
	 */
	methods: {

	},

	/**
	 * Service created lifecycle event handler
	 */
	created() {

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
